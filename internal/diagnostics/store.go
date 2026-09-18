package diagnostics

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	_ "modernc.org/sqlite"
)

type store struct{ db *sql.DB }

const diskLimit = 256 << 20

// Open is idempotent; storage is initialized lazily by administrator endpoints.
func (m *Manager) Open(path string) error {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.store != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Chmod(path, 0600); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		"PRAGMA busy_timeout=3000", "PRAGMA journal_mode=DELETE", "PRAGMA secure_delete=ON",
		"CREATE TABLE IF NOT EXISTS captures(id TEXT PRIMARY KEY,trace_id TEXT NOT NULL,expires_at INTEGER NOT NULL,body BLOB NOT NULL)",
		"CREATE INDEX IF NOT EXISTS captures_trace ON captures(trace_id)",
	} {
		if _, err = db.Exec(statement); err != nil {
			_ = db.Close()
			return err
		}
	}
	m.store = &store{db: db}
	m.closing = false
	m.queue = make(chan *Attempt, attemptLimit)
	m.stop = make(chan struct{})
	m.done = make(chan struct{})
	go m.worker()
	return nil
}

func (m *Manager) Enable() error {
	return m.EnableFor(Window)
}

func (m *Manager) EnableFor(window time.Duration) error {
	if window != 10*time.Second && window != 20*time.Second && window != 30*time.Second {
		return errors.New("capture duration must be 10, 20 or 30 seconds")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.store == nil || m.closing {
		return errors.New("capture storage unavailable")
	}
	if m.now().Before(time.Unix(0, m.until.Load())) {
		return nil
	}
	m.count = 0
	m.dropped = 0
	m.window = window
	m.until.Store(m.now().Add(window).UnixNano())
	return nil
}

func (m *Manager) worker() {
	defer close(m.done)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	persist := func(a *Attempt) {
		// Finished attempts are immutable until removed from the active map.
		data, err := json.Marshal(a.data)
		if err == nil {
			_, err = m.store.db.Exec("INSERT OR REPLACE INTO captures VALUES(?,?,?,?)", a.data.ID, a.data.TraceID, a.data.StartedAt.Add(Retention).Unix(), data)
		}
		m.mu.Lock()
		if err != nil {
			m.storageErrors++
		}
		delete(m.active, a.data.ID)
		m.memory -= a.bytes
		a.clearPayloadLocked()
		m.mu.Unlock()
		m.cleanup()
	}
	m.cleanup()
	for {
		select {
		case a := <-m.queue:
			persist(a)
		case <-ticker.C:
			m.cleanup()
		case <-m.stop:
			for {
				select {
				case a := <-m.queue:
					persist(a)
				default:
					return
				}
			}
		}
	}
}

func (m *Manager) cleanup() {
	_, err := m.store.db.Exec("DELETE FROM captures WHERE expires_at<=?", m.now().Unix())
	if err == nil {
		_, err = m.store.db.Exec(`DELETE FROM captures WHERE id IN (
   SELECT id FROM (SELECT id,SUM(length(body)) OVER (ORDER BY expires_at DESC,id DESC) AS bytes FROM captures) WHERE bytes>?
  )`, diskLimit)
	}
	if err != nil {
		m.mu.Lock()
		m.storageErrors++
		m.mu.Unlock()
	}
}

func cloneCapture(c Capture) Capture {
	c.RequestHeaders = c.RequestHeaders.Clone()
	c.RequestBody = append([]byte(nil), c.RequestBody...)
	c.ResponseBody = append([]byte(nil), c.ResponseBody...)
	c.ResponseHeaders = c.ResponseHeaders.Clone()
	c.ResponseTrailers = c.ResponseTrailers.Clone()
	c.Frames = append([]Frame(nil), c.Frames...)
	for i := range c.Frames {
		c.Frames[i].Body = append([]byte(nil), c.Frames[i].Body...)
	}
	return c
}

func (m *Manager) Get(traceID string) ([]Capture, error) {
	// Snapshot active attempts before querying disk to avoid a completion race.
	m.mu.Lock()
	if m.store == nil {
		m.mu.Unlock()
		return nil, errors.New("capture storage unavailable")
	}
	db := m.store.db
	byID := make(map[string]Capture)
	for _, a := range m.active {
		if a.data.TraceID == traceID {
			byID[a.data.ID] = cloneCapture(a.data)
		}
	}
	m.mu.Unlock()
	rows, err := db.Query("SELECT body FROM captures WHERE trace_id=? AND expires_at>?", traceID, m.now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var data []byte
		var c Capture
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &c); err != nil {
			return nil, err
		}
		byID[c.ID] = c
	}
	out := make([]Capture, 0, len(byID))
	for _, c := range byID {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out, rows.Err()
}

// Delete refuses in-flight data; otherwise a queued write could recreate it.
func (m *Manager) Delete(traceID string) error {
	m.mu.Lock()
	for _, a := range m.active {
		if a.data.TraceID == traceID {
			m.mu.Unlock()
			return errors.New("capture still active or saving")
		}
	}
	if m.store == nil {
		m.mu.Unlock()
		return errors.New("capture storage unavailable")
	}
	db := m.store.db
	m.mu.Unlock()
	_, err := db.Exec("DELETE FROM captures WHERE trace_id=?", traceID)
	return err
}

func (m *Manager) Close() {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.Disable()
	m.mu.Lock()
	m.closing = true
	if m.store == nil {
		m.mu.Unlock()
		return
	}
	pending := make([]*Attempt, 0, len(m.active))
	for _, a := range m.active {
		pending = append(pending, a)
	}
	m.mu.Unlock()
	for _, a := range pending {
		a.Finish("shutdown", true)
	}
	close(m.stop)
	<-m.done
	_ = m.store.db.Close()
	m.mu.Lock()
	m.store = nil
	m.mu.Unlock()
}
