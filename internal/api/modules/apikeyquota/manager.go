package apikeyquota

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"

	_ "modernc.org/sqlite"
)

const (
	sqlitePathEnv     = "APIKEY_QUOTA_SQLITE_PATH"
	defaultSQLitePath = "apikeyquota.sqlite3"
	defaultFlushEvery = 5 * time.Second
)

// Decision describes whether a downstream API key can start another request.
type Decision struct {
	Allowed         bool
	APIKey          string
	DailyTokenLimit int64
	UsedTokens      int64
	RemainingTokens int64
	Day             string
	ResetAt         time.Time
}

// Status describes the current daily quota state for a configured downstream API key.
type Status struct {
	APIKey          string    `json:"api-key"`
	Remark          string    `json:"remark,omitempty"`
	DailyTokenLimit int64     `json:"daily-token-limit"`
	UsedTokens      int64     `json:"used-tokens"`
	RemainingTokens int64     `json:"remaining-tokens"`
	RequestCount    int64     `json:"request-count"`
	Day             string    `json:"day"`
	ResetAt         time.Time `json:"reset-at"`
	Limited         bool      `json:"limited"`
	Exceeded        bool      `json:"exceeded"`
}

// Manager enforces and records daily downstream API key token quotas.
type Manager struct {
	mu    sync.RWMutex
	cfg   *config.Config
	store *sqliteStore
	loc   *time.Location

	usageMu   sync.RWMutex
	loadMu    sync.Mutex
	counters  map[usageKey]*usageCounter
	loadedDay map[string]struct{}

	dirtyMu sync.Mutex
	dirty   map[usageKey]dailyUsage

	flushEvery time.Duration
	stopFlush  chan struct{}
	flushDone  chan struct{}
	closeOnce  sync.Once
}

// NewManager creates a manager with the default SQLite-backed quota store.
func NewManager(cfg *config.Config) *Manager {
	store, err := newSQLiteStore(defaultStorePath())
	if err != nil {
		log.Warnf("apikeyquota: sqlite store unavailable: %v", err)
	}
	manager := &Manager{
		cfg:        cfg,
		store:      store,
		loc:        time.Local,
		flushEvery: defaultFlushEvery,
	}
	manager.initUsageMaps()
	if _, errLoad := manager.ensureDayLoaded(time.Now()); errLoad != nil {
		log.Warnf("apikeyquota: failed to load today's usage: %v", errLoad)
	}
	manager.startFlushLoop()
	return manager
}

func defaultStorePath() string {
	if path := strings.TrimSpace(os.Getenv(sqlitePathEnv)); path != "" {
		return path
	}
	return defaultSQLitePath
}

// SetConfig updates the runtime config reference after hot reload.
func (m *Manager) SetConfig(cfg *config.Config) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.cfg = cfg
	m.mu.Unlock()
}

// Check returns whether the given downstream API key is still under its daily limit.
func (m *Manager) Check(apiKey string, now time.Time) (Decision, error) {
	apiKey = strings.TrimSpace(apiKey)
	if m == nil || apiKey == "" {
		return Decision{Allowed: true}, nil
	}
	limit := m.limitForAPIKey(apiKey)
	day, resetAt := m.dayWindow(now)
	decision := Decision{
		Allowed:         true,
		APIKey:          apiKey,
		DailyTokenLimit: limit,
		Day:             day,
		ResetAt:         resetAt,
	}
	if limit <= 0 {
		return decision, nil
	}
	if _, err := m.ensureDayLoaded(now); err != nil {
		return decision, err
	}
	usage := m.usageForKey(usageKey{APIKeyHash: hashAPIKey(apiKey), Day: day})
	decision.UsedTokens = usage.UsedTokens
	decision.RemainingTokens = remainingTokens(limit, usage.UsedTokens)
	decision.Allowed = usage.UsedTokens < limit
	return decision, nil
}

// Statuses returns daily quota status for every configured downstream API key.
func (m *Manager) Statuses(now time.Time) ([]Status, error) {
	if m == nil {
		return nil, nil
	}
	entries := m.apiKeyEntries()
	day, resetAt := m.dayWindow(now)
	if _, err := m.ensureDayLoaded(now); err != nil {
		return nil, err
	}
	statuses := make([]Status, 0, len(entries))
	for _, entry := range entries {
		apiKey := strings.TrimSpace(entry.APIKey)
		if apiKey == "" {
			continue
		}
		usage := m.usageForKey(usageKey{APIKeyHash: hashAPIKey(apiKey), Day: day})
		limit := entry.DailyTokenLimit
		statuses = append(statuses, Status{
			APIKey:          apiKey,
			Remark:          strings.TrimSpace(entry.Remark),
			DailyTokenLimit: limit,
			UsedTokens:      usage.UsedTokens,
			RemainingTokens: remainingTokens(limit, usage.UsedTokens),
			RequestCount:    usage.RequestCount,
			Day:             day,
			ResetAt:         resetAt,
			Limited:         limit > 0,
			Exceeded:        limit > 0 && usage.UsedTokens >= limit,
		})
	}
	return statuses, nil
}

// Lookup returns the daily quota status for one configured downstream API key.
func (m *Manager) Lookup(apiKey string, now time.Time) (Status, bool, error) {
	apiKey = strings.TrimSpace(apiKey)
	if m == nil || apiKey == "" {
		return Status{}, false, nil
	}
	entries := m.apiKeyEntries()
	var matched config.APIKeyEntry
	found := false
	for _, entry := range entries {
		if strings.TrimSpace(entry.APIKey) == apiKey {
			matched = entry
			found = true
			break
		}
	}
	if !found {
		return Status{}, false, nil
	}
	day, resetAt := m.dayWindow(now)
	if _, err := m.ensureDayLoaded(now); err != nil {
		return Status{}, false, err
	}
	usage := m.usageForKey(usageKey{APIKeyHash: hashAPIKey(apiKey), Day: day})
	limit := matched.DailyTokenLimit
	return Status{
		APIKey:          apiKey,
		Remark:          strings.TrimSpace(matched.Remark),
		DailyTokenLimit: limit,
		UsedTokens:      usage.UsedTokens,
		RemainingTokens: remainingTokens(limit, usage.UsedTokens),
		RequestCount:    usage.RequestCount,
		Day:             day,
		ResetAt:         resetAt,
		Limited:         limit > 0,
		Exceeded:        limit > 0 && usage.UsedTokens >= limit,
	}, true, nil
}

// HandleUsage records completed usage into the independent quota ledger.
func (m *Manager) HandleUsage(_ context.Context, record coreusage.Record) {
	if m == nil {
		return
	}
	apiKey := strings.TrimSpace(record.APIKey)
	if apiKey == "" || !m.hasAPIKey(apiKey) {
		return
	}
	totalTokens := normalizeTotalTokens(record.Detail)
	if totalTokens <= 0 {
		return
	}
	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	day, err := m.ensureDayLoaded(timestamp)
	if err != nil {
		log.Warnf("apikeyquota: failed to load usage day before recording: %v", err)
		return
	}
	key := usageKey{APIKeyHash: hashAPIKey(apiKey), Day: day}
	counter := m.counterForKey(key)
	counter.usedTokens.Add(totalTokens)
	counter.requestCount.Add(1)
	m.markDirty(key, dailyUsage{UsedTokens: totalTokens, RequestCount: 1})
}

// Flush persists pending in-memory quota usage deltas to SQLite.
func (m *Manager) Flush() error {
	if m == nil || m.store == nil {
		return nil
	}
	batch := m.takeDirty()
	if len(batch) == 0 {
		return nil
	}
	if err := m.store.AddMany(batch, time.Now()); err != nil {
		m.mergeDirty(batch)
		return err
	}
	return nil
}

// Close stops the periodic flush worker, flushes pending usage, and closes the SQLite store.
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	var err error
	m.closeOnce.Do(func() {
		if m.stopFlush != nil {
			close(m.stopFlush)
			if m.flushDone != nil {
				<-m.flushDone
			}
		}
		err = m.Flush()
		if m.store != nil {
			if errClose := m.store.Close(); errClose != nil && err == nil {
				err = errClose
			}
		}
	})
	return err
}

func (m *Manager) startFlushLoop() {
	if m == nil || m.flushEvery <= 0 {
		return
	}
	m.stopFlush = make(chan struct{})
	m.flushDone = make(chan struct{})
	go func() {
		ticker := time.NewTicker(m.flushEvery)
		defer ticker.Stop()
		defer close(m.flushDone)
		for {
			select {
			case <-ticker.C:
				if err := m.Flush(); err != nil {
					log.Warnf("apikeyquota: failed to flush usage: %v", err)
				}
			case <-m.stopFlush:
				if err := m.Flush(); err != nil {
					log.Warnf("apikeyquota: failed to flush usage during close: %v", err)
				}
				return
			}
		}
	}()
}

func (m *Manager) initUsageMaps() {
	if m == nil {
		return
	}
	m.usageMu.Lock()
	m.ensureUsageMapsLocked()
	m.usageMu.Unlock()
	m.dirtyMu.Lock()
	if m.dirty == nil {
		m.dirty = make(map[usageKey]dailyUsage)
	}
	m.dirtyMu.Unlock()
}

func (m *Manager) ensureUsageMapsLocked() {
	if m.counters == nil {
		m.counters = make(map[usageKey]*usageCounter)
	}
	if m.loadedDay == nil {
		m.loadedDay = make(map[string]struct{})
	}
}

func (m *Manager) ensureDayLoaded(now time.Time) (string, error) {
	if m == nil {
		return "", nil
	}
	day, _ := m.dayWindow(now)
	if day == "" {
		return "", nil
	}

	m.usageMu.RLock()
	_, loaded := m.loadedDay[day]
	m.usageMu.RUnlock()
	if loaded {
		return day, nil
	}

	m.loadMu.Lock()
	defer m.loadMu.Unlock()

	m.usageMu.RLock()
	_, loaded = m.loadedDay[day]
	m.usageMu.RUnlock()
	if loaded {
		return day, nil
	}

	rows := map[string]dailyUsage(nil)
	if m.store != nil {
		var err error
		rows, err = m.store.ListDay(day)
		if err != nil {
			return day, err
		}
	}

	m.usageMu.Lock()
	m.ensureUsageMapsLocked()
	for apiKeyHash, usage := range rows {
		key := usageKey{APIKeyHash: apiKeyHash, Day: day}
		if _, exists := m.counters[key]; !exists {
			m.counters[key] = newUsageCounter(usage)
		}
	}
	m.loadedDay[day] = struct{}{}
	m.usageMu.Unlock()

	return day, nil
}

func (m *Manager) usageForKey(key usageKey) dailyUsage {
	if m == nil {
		return dailyUsage{}
	}
	m.usageMu.RLock()
	counter := m.counters[key]
	m.usageMu.RUnlock()
	if counter == nil {
		return dailyUsage{}
	}
	return counter.snapshot()
}

func (m *Manager) counterForKey(key usageKey) *usageCounter {
	if m == nil {
		return newUsageCounter(dailyUsage{})
	}
	m.usageMu.RLock()
	counter := m.counters[key]
	m.usageMu.RUnlock()
	if counter != nil {
		return counter
	}

	m.usageMu.Lock()
	m.ensureUsageMapsLocked()
	counter = m.counters[key]
	if counter == nil {
		counter = newUsageCounter(dailyUsage{})
		m.counters[key] = counter
	}
	m.usageMu.Unlock()
	return counter
}

func (m *Manager) markDirty(key usageKey, delta dailyUsage) {
	if m == nil || m.store == nil || delta.UsedTokens <= 0 {
		return
	}
	m.dirtyMu.Lock()
	if m.dirty == nil {
		m.dirty = make(map[usageKey]dailyUsage)
	}
	existing := m.dirty[key]
	existing.UsedTokens += delta.UsedTokens
	existing.RequestCount += delta.RequestCount
	m.dirty[key] = existing
	m.dirtyMu.Unlock()
}

func (m *Manager) takeDirty() map[usageKey]dailyUsage {
	if m == nil {
		return nil
	}
	m.dirtyMu.Lock()
	defer m.dirtyMu.Unlock()
	if len(m.dirty) == 0 {
		return nil
	}
	batch := m.dirty
	m.dirty = make(map[usageKey]dailyUsage)
	return batch
}

func (m *Manager) mergeDirty(batch map[usageKey]dailyUsage) {
	if m == nil || len(batch) == 0 {
		return
	}
	m.dirtyMu.Lock()
	if m.dirty == nil {
		m.dirty = make(map[usageKey]dailyUsage, len(batch))
	}
	for key, delta := range batch {
		existing := m.dirty[key]
		existing.UsedTokens += delta.UsedTokens
		existing.RequestCount += delta.RequestCount
		m.dirty[key] = existing
	}
	m.dirtyMu.Unlock()
}

func (m *Manager) limitForAPIKey(apiKey string) int64 {
	entries := m.apiKeyEntries()
	for _, entry := range entries {
		if entry.APIKey == apiKey {
			return entry.DailyTokenLimit
		}
	}
	return 0
}

func (m *Manager) hasAPIKey(apiKey string) bool {
	entries := m.apiKeyEntries()
	for _, entry := range entries {
		if entry.APIKey == apiKey {
			return true
		}
	}
	return false
}

func (m *Manager) apiKeyEntries() config.APIKeyEntryList {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	cfg := m.cfg
	m.mu.RUnlock()
	if cfg == nil {
		return nil
	}
	return cfg.APIKeyEntriesSnapshot()
}

func (m *Manager) dayWindow(now time.Time) (string, time.Time) {
	if now.IsZero() {
		now = time.Now()
	}
	loc := time.Local
	if m != nil && m.loc != nil {
		loc = m.loc
	}
	local := now.In(loc)
	day := local.Format("2006-01-02")
	resetAt := time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, loc)
	return day, resetAt
}

func normalizeTotalTokens(detail coreusage.Detail) int64 {
	if detail.TotalTokens > 0 {
		return detail.TotalTokens
	}
	total := detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens
	if total > 0 {
		return total
	}
	total = detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens + detail.CachedTokens
	if total > 0 {
		return total
	}
	return 0
}

func remainingTokens(limit, used int64) int64 {
	if limit <= 0 {
		return 0
	}
	remaining := limit - used
	if remaining < 0 {
		return 0
	}
	return remaining
}

func hashAPIKey(apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:])
}

type dailyUsage struct {
	UsedTokens   int64
	RequestCount int64
}

type usageKey struct {
	APIKeyHash string
	Day        string
}

type usageCounter struct {
	usedTokens   atomic.Int64
	requestCount atomic.Int64
}

func newUsageCounter(usage dailyUsage) *usageCounter {
	counter := &usageCounter{}
	counter.usedTokens.Store(usage.UsedTokens)
	counter.requestCount.Store(usage.RequestCount)
	return counter
}

func (c *usageCounter) snapshot() dailyUsage {
	if c == nil {
		return dailyUsage{}
	}
	return dailyUsage{
		UsedTokens:   c.usedTokens.Load(),
		RequestCount: c.requestCount.Load(),
	}
}

type sqliteStore struct {
	db *sql.DB
}

func newSQLiteStore(path string) (*sqliteStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("empty sqlite path")
	}
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create sqlite directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite store: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &sqliteStore{db: db}
	if err := store.init(); err != nil {
		if closeErr := db.Close(); closeErr != nil {
			return nil, fmt.Errorf("init sqlite store: %w; close sqlite: %v", err, closeErr)
		}
		return nil, err
	}
	return store, nil
}

func (s *sqliteStore) init() error {
	if s == nil || s.db == nil {
		return nil
	}
	statements := []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA synchronous = NORMAL`,
		`PRAGMA busy_timeout = 5000`,
		`CREATE TABLE IF NOT EXISTS api_key_daily_usage (
			api_key_hash TEXT NOT NULL,
			day TEXT NOT NULL,
			used_tokens INTEGER NOT NULL DEFAULT 0,
			request_count INTEGER NOT NULL DEFAULT 0,
			updated_at_ns INTEGER NOT NULL,
			PRIMARY KEY(api_key_hash, day)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_api_key_daily_usage_day ON api_key_daily_usage(day)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("sqlite init statement failed: %w", err)
		}
	}
	return nil
}

func (s *sqliteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *sqliteStore) Get(apiKeyHash, day string) (dailyUsage, error) {
	if s == nil || s.db == nil {
		return dailyUsage{}, nil
	}
	row := s.db.QueryRow(
		`SELECT used_tokens, request_count FROM api_key_daily_usage WHERE api_key_hash = ? AND day = ?`,
		apiKeyHash,
		day,
	)
	var usage dailyUsage
	if err := row.Scan(&usage.UsedTokens, &usage.RequestCount); err != nil {
		if err == sql.ErrNoRows {
			return dailyUsage{}, nil
		}
		return dailyUsage{}, err
	}
	return usage, nil
}

func (s *sqliteStore) ListDay(day string) (map[string]dailyUsage, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.Query(
		`SELECT api_key_hash, used_tokens, request_count FROM api_key_daily_usage WHERE day = ?`,
		day,
	)
	if err != nil {
		return nil, err
	}
	defer func() {
		if errClose := rows.Close(); errClose != nil {
			log.Warnf("apikeyquota: failed to close sqlite rows: %v", errClose)
		}
	}()

	out := make(map[string]dailyUsage)
	for rows.Next() {
		var apiKeyHash string
		var usage dailyUsage
		if errScan := rows.Scan(&apiKeyHash, &usage.UsedTokens, &usage.RequestCount); errScan != nil {
			return nil, errScan
		}
		apiKeyHash = strings.TrimSpace(apiKeyHash)
		if apiKeyHash == "" {
			continue
		}
		out[apiKeyHash] = usage
	}
	if errRows := rows.Err(); errRows != nil {
		return nil, errRows
	}
	return out, nil
}

func (s *sqliteStore) Add(apiKeyHash, day string, usedTokens, requestCount int64, updatedAt time.Time) error {
	return s.AddMany(map[usageKey]dailyUsage{
		{APIKeyHash: apiKeyHash, Day: day}: {
			UsedTokens:   usedTokens,
			RequestCount: requestCount,
		},
	}, updatedAt)
}

func (s *sqliteStore) AddMany(batch map[usageKey]dailyUsage, updatedAt time.Time) error {
	if s == nil || s.db == nil || len(batch) == 0 {
		return nil
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	stmt, err := tx.Prepare(
		`INSERT INTO api_key_daily_usage (api_key_hash, day, used_tokens, request_count, updated_at_ns)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(api_key_hash, day) DO UPDATE SET
			used_tokens = used_tokens + excluded.used_tokens,
			request_count = request_count + excluded.request_count,
			updated_at_ns = excluded.updated_at_ns`,
	)
	if err != nil {
		return err
	}
	defer func() {
		if errClose := stmt.Close(); errClose != nil {
			log.Warnf("apikeyquota: failed to close sqlite statement: %v", errClose)
		}
	}()

	updatedAtNS := updatedAt.UTC().UnixNano()
	for key, usage := range batch {
		apiKeyHash := strings.TrimSpace(key.APIKeyHash)
		day := strings.TrimSpace(key.Day)
		usedTokens := usage.UsedTokens
		requestCount := usage.RequestCount
		if apiKeyHash == "" || day == "" || usedTokens <= 0 {
			continue
		}
		if requestCount <= 0 {
			requestCount = 1
		}
		if _, errExec := stmt.Exec(apiKeyHash, day, usedTokens, requestCount, updatedAtNS); errExec != nil {
			return errExec
		}
	}
	if errCommit := tx.Commit(); errCommit != nil {
		return errCommit
	}
	committed = true
	return nil
}
