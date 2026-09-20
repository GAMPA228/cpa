// Package turnstate maintains temporary, account/model-scoped upstream state.
package turnstate

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const Header = "X-Codex-Turn-State"
const Lifetime = time.Hour
const RefreshBefore = 5 * time.Minute
const MaxChars = 8192

type Settings struct {
	Enabled  bool `json:"enabled"`
	MaxChars int  `json:"max_chars"`
}

type Status struct {
	Settings
	StorageErrors uint64 `json:"storage_errors"`
	Dropped       uint64 `json:"dropped"`
}

type Rule struct {
	AuthID    string    `json:"auth_id"`
	Model     string    `json:"model"`
	Value     string    `json:"value"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (r Rule) ID() string {
	sum := sha256.Sum256([]byte(r.AuthID + "\x00" + r.Model))
	return fmt.Sprintf("auto-turn-state-%x", sum[:16])
}

type key struct{ auth, model string }
type observation struct {
	rule       Rule
	generation uint64
}

type Manager struct {
	mu            sync.RWMutex
	writeMu       sync.Mutex
	settings      Settings
	rules         map[key]Rule
	refreshes     map[key]*Refresh
	refreshSeq    uint64
	generation    uint64
	storageErrors uint64
	dropped       uint64
	store         *store
	queue         chan observation
	done          chan struct{}
	closing       bool
	workerCtx     context.Context
	cancelWorker  context.CancelFunc
	now           func() time.Time
}

var Default = NewManager()

func NewManager() *Manager {
	return &Manager{settings: Settings{MaxChars: 292}, rules: make(map[key]Rule), now: time.Now}
}

// Value rejects ambiguous multi-valued headers. Length is measured before decoding.
func Value(headers http.Header) (string, *int) {
	var values []string
	for name, v := range headers {
		if strings.EqualFold(name, Header) {
			values = append(values, v...)
		}
	}
	if len(values) != 1 {
		return "", nil
	}
	value := strings.TrimSpace(values[0])
	if value == "" {
		return "", nil
	}
	n := len(value)
	return value, &n
}

// Parse checks only the public Fernet envelope, never the signature or ciphertext.
// Expiry is our local policy; the upstream token does not declare its lifetime.
func Parse(value string, now time.Time) (time.Time, bool) {
	if len(value) == 0 || len(value) > MaxChars || strings.ContainsAny(value, "\r\n \t") {
		return time.Time{}, false
	}
	data, err := base64.URLEncoding.Strict().DecodeString(value)
	if err != nil {
		data, err = base64.RawURLEncoding.Strict().DecodeString(value)
	}
	if err != nil || len(data) < 73 || data[0] != 0x80 || (len(data)-57)%16 != 0 {
		return time.Time{}, false
	}
	seconds := binary.BigEndian.Uint64(data[1:9])
	if seconds > uint64(now.Unix()+60) {
		return time.Time{}, false
	}
	issued := time.Unix(int64(seconds), 0).UTC()
	return issued, now.Before(issued.Add(Lifetime))
}

func (m *Manager) Status() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return Status{Settings: m.settings, StorageErrors: m.storageErrors, Dropped: m.dropped}
}

func (m *Manager) eligibleLocked(r Rule, now time.Time) bool {
	if !m.settings.Enabled || len(r.Value) > m.settings.MaxChars || !now.Before(r.ExpiresAt) {
		return false
	}
	previous, exists := m.rules[key{r.AuthID, r.Model}]
	if !exists {
		return true
	}
	if !r.IssuedAt.After(previous.IssuedAt) {
		return false
	}
	return len(previous.Value) > m.settings.MaxChars || !now.Before(previous.ExpiresAt.Add(-RefreshBefore))
}

// Observe only queues observations from fresh upstream responses. It never reads disk.
func (m *Manager) Observe(authID, model, value string) {
	if authID == "" || model == "" || len(model) > 256 {
		return
	}
	m.mu.RLock()
	enabled := m.store != nil && !m.closing && m.settings.Enabled && len(value) <= m.settings.MaxChars
	m.mu.RUnlock()
	if !enabled {
		return
	}
	now := m.now()
	issued, valid := Parse(value, now)
	if !valid {
		return
	}
	r := Rule{AuthID: authID, Model: model, Value: value, IssuedAt: issued, ExpiresAt: issued.Add(Lifetime)}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.store == nil || m.closing || !m.eligibleLocked(r, now) {
		return
	}
	select {
	case m.queue <- observation{rule: r, generation: m.generation}:
	default:
		m.dropped++
	}
}

// Lookup is used for each outgoing request, so switches and thresholds apply immediately.
func (m *Manager) Lookup(authID, model string) (Rule, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.rules[key{authID, model}]
	return r, ok && m.settings.Enabled && len(r.Value) <= m.settings.MaxChars && m.now().Before(r.ExpiresAt)
}

func (m *Manager) Rules(authID string) []Rule {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var rules []Rule
	for k, r := range m.rules {
		if k.auth == authID {
			rules = append(rules, r)
		}
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].Model < rules[j].Model })
	return rules
}
