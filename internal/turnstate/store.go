package turnstate

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type store struct{ db *sql.DB }

func (m *Manager) Open(path string) error {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
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
	success := false
	defer func() {
		if !success {
			_ = db.Close()
		}
	}()
	for _, query := range []string{
		"PRAGMA busy_timeout=3000", "PRAGMA journal_mode=DELETE", "PRAGMA secure_delete=ON",
		"CREATE TABLE IF NOT EXISTS settings(id INTEGER PRIMARY KEY CHECK(id=1), enabled INTEGER NOT NULL, max_chars INTEGER NOT NULL)",
		"INSERT OR IGNORE INTO settings VALUES(1,0,292)",
		"CREATE TABLE IF NOT EXISTS rules(auth_id TEXT NOT NULL, model TEXT NOT NULL, value TEXT NOT NULL, issued_at INTEGER NOT NULL, PRIMARY KEY(auth_id,model))",
	} {
		if _, err = db.Exec(query); err != nil {
			return err
		}
	}
	var settings Settings
	if err = db.QueryRow("SELECT enabled,max_chars FROM settings WHERE id=1").Scan(&settings.Enabled, &settings.MaxChars); err != nil {
		return err
	}
	if settings.MaxChars < 1 || settings.MaxChars > MaxChars {
		return errors.New("invalid turn state settings")
	}
	rows, err := db.Query("SELECT auth_id,model,value,issued_at FROM rules")
	if err != nil {
		return err
	}
	rules := make(map[key]Rule)
	for rows.Next() {
		var r Rule
		var issued int64
		if err = rows.Scan(&r.AuthID, &r.Model, &r.Value, &issued); err != nil {
			_ = rows.Close()
			return err
		}
		r.IssuedAt = time.Unix(issued, 0).UTC()
		r.ExpiresAt = r.IssuedAt.Add(Lifetime)
		// Retain expired entries for the rules page, but reject malformed stored tokens.
		if parsed, valid := Parse(r.Value, r.IssuedAt); valid && parsed.Equal(r.IssuedAt) {
			rules[key{r.AuthID, r.Model}] = r
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	m.settings = settings
	m.rules = rules
	m.store = &store{db: db}
	m.closing = false
	m.queue = make(chan observation, 128)
	m.done = make(chan struct{})
	m.workerCtx, m.cancelWorker = context.WithCancel(context.Background())
	success = true
	go m.worker()
	return nil
}

func (m *Manager) Configure(settings Settings) error {
	if settings.MaxChars < 1 || settings.MaxChars > MaxChars {
		return errors.New("max_chars must be an integer between 1 and 8192")
	}
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	m.mu.RLock()
	s, closing := m.store, m.closing
	m.mu.RUnlock()
	if s == nil || closing {
		return errors.New("turn state storage unavailable")
	}
	if _, err := s.db.Exec("UPDATE settings SET enabled=?,max_chars=? WHERE id=1", settings.Enabled, settings.MaxChars); err != nil {
		return err
	}
	m.mu.Lock()
	m.settings = settings
	m.generation++
	m.mu.Unlock()
	return nil
}

func (m *Manager) worker() {
	defer close(m.done)
	for obs := range m.queue {
		m.persist(obs)
	}
}

func (m *Manager) persist(obs observation) {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	if m.workerCtx.Err() != nil {
		return
	}
	m.mu.RLock()
	r := obs.rule
	now := m.now()
	eligible := obs.generation == m.generation && m.eligibleLocked(r, now)
	// Bound model growth per account, independently of the manual rule limit.
	count := 0
	var reclaim *Rule
	for k, old := range m.rules {
		if k.auth == r.AuthID {
			count++
			if !now.Before(old.ExpiresAt) && (reclaim == nil || old.ExpiresAt.Before(reclaim.ExpiresAt)) {
				copy := old
				reclaim = &copy
			}
		}
	}
	_, exists := m.rules[key{r.AuthID, r.Model}]
	full := !exists && count >= 128
	m.mu.RUnlock()
	if !eligible {
		return
	}
	if full && reclaim == nil {
		m.mu.Lock()
		m.dropped++
		m.mu.Unlock()
		return
	}
	tx, err := m.store.db.BeginTx(m.workerCtx, nil)
	if err == nil {
		defer func() { _ = tx.Rollback() }()
		if full {
			_, err = tx.ExecContext(m.workerCtx, "DELETE FROM rules WHERE auth_id=? AND model=?", reclaim.AuthID, reclaim.Model)
		}
		if err == nil {
			_, err = tx.ExecContext(m.workerCtx, "INSERT INTO rules(auth_id,model,value,issued_at) VALUES(?,?,?,?) ON CONFLICT(auth_id,model) DO UPDATE SET value=excluded.value,issued_at=excluded.issued_at", r.AuthID, r.Model, r.Value, r.IssuedAt.Unix())
		}
		if err == nil {
			err = tx.Commit()
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.storageErrors++
		return
	}
	if full {
		delete(m.rules, key{reclaim.AuthID, reclaim.Model})
	}
	m.rules[key{r.AuthID, r.Model}] = r
}

// Close drains queued observations before closing the database.
func (m *Manager) Close() {
	m.CloseContext(context.Background())
}

// CloseContext cancels queued persistence when the service shutdown budget expires.
func (m *Manager) CloseContext(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	if m.store == nil || m.closing {
		m.mu.Unlock()
		return
	}
	m.closing = true
	close(m.queue)
	done := m.done
	m.mu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
		m.cancelWorker()
		<-done
	}
	m.cancelWorker()
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.store.db.Close(); err != nil {
		m.storageErrors++
	}
	m.store = nil
}
