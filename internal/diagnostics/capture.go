package diagnostics

import (
	"context"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

const (
	Window        = 10 * time.Second
	Lifetime      = 5 * time.Minute
	Retention     = 24 * time.Hour
	requestLimit  = 2 << 20
	responseLimit = 8 << 20
	memoryLimit   = 64 << 20
	attemptLimit  = 100
)

var Default = NewManager()

type Metadata struct {
	URL      string `json:"url"`
	Method   string `json:"method"`
	Provider string `json:"provider"`
	AuthID   string `json:"auth_id"`
	Proxy    string `json:"proxy"`
}

type Frame struct {
	At   time.Time `json:"at"`
	Body []byte    `json:"body"`
}

type Capture struct {
	ID      string `json:"id"`
	TraceID string `json:"trace_id"`
	Metadata
	StartedAt             time.Time   `json:"started_at"`
	FinishedAt            *time.Time  `json:"finished_at"`
	Protocol              string      `json:"protocol"`
	Status                int         `json:"status"`
	ResponseHeaders       http.Header `json:"response_headers"`
	ResponseTrailers      http.Header `json:"response_trailers,omitempty"`
	HandshakeReused       bool        `json:"handshake_reused"`
	TransportDecompressed bool        `json:"transport_decompressed"`
	RequestBody           []byte      `json:"request_body"`
	ResponseBody          []byte      `json:"response_body"`
	Frames                []Frame     `json:"frames,omitempty"`
	Truncated             bool        `json:"truncated"`
	Reason                string      `json:"reason,omitempty"`
}

type Status struct {
	Enabled       bool      `json:"enabled"`
	Until         time.Time `json:"until"`
	Captured      int       `json:"captured"`
	Dropped       int       `json:"dropped"`
	Active        int       `json:"active"`
	StorageErrors int       `json:"storage_errors"`
}

type Manager struct {
	lifecycleMu   sync.Mutex
	closing       bool
	mu            sync.Mutex
	until         atomic.Int64
	count         int
	dropped       int
	storageErrors int
	memory        int
	active        map[string]*Attempt
	queue         chan *Attempt
	stop          chan struct{}
	done          chan struct{}
	store         *store
	now           func() time.Time
}

func NewManager() *Manager {
	return &Manager{active: make(map[string]*Attempt), now: time.Now}
}

type contextKey struct{}
type trace struct {
	mu      sync.Mutex
	id      string
	manager *Manager
	current *Attempt
	proxy   string
}

func (m *Manager) Context(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if !m.now().Before(time.Unix(0, m.until.Load())) {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, &trace{id: uuid.NewString(), manager: m})
}

func from(ctx context.Context) *trace {
	if ctx == nil {
		return nil
	}
	t, _ := ctx.Value(contextKey{}).(*trace)
	return t
}

func ID(ctx context.Context) string {
	if t := from(ctx); t != nil {
		return t.id
	}
	return ""
}

func SetProxy(ctx context.Context, proxy string) {
	if t := from(ctx); t != nil {
		t.mu.Lock()
		t.proxy = proxy
		t.mu.Unlock()
	}
}

func Current(ctx context.Context) *Attempt {
	if t := from(ctx); t != nil {
		t.mu.Lock()
		defer t.mu.Unlock()
		return t.current
	}
	return nil
}

func Begin(ctx context.Context, meta Metadata, body []byte, websocket bool) *Attempt {
	t := from(ctx)
	if t == nil {
		return nil
	}
	m := t.manager
	t.mu.Lock()
	defer t.mu.Unlock()
	// Clear a previous attempt even when this attempt falls outside the window.
	if t.current != nil {
		t.current.Finish("superseded", true)
	}
	t.current = nil
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if m.store == nil || m.closing || !now.Before(time.Unix(0, m.until.Load())) {
		return nil
	}
	if m.count >= attemptLimit || m.memory >= memoryLimit {
		m.dropped++
		return nil
	}
	parsed, err := url.Parse(meta.URL)
	if err == nil {
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.Fragment = ""
		meta.URL = parsed.String()
	} else {
		meta.URL = ""
	}
	meta.Proxy = t.proxy
	protocol := "http"
	if websocket {
		protocol = "websocket"
	}
	a := &Attempt{manager: m, data: Capture{ID: uuid.NewString(), TraceID: t.id, Metadata: meta, StartedAt: now, Protocol: protocol}}
	m.count++
	m.active[a.data.ID] = a
	a.appendLocked(body, true)
	a.timer = time.AfterFunc(Lifetime, func() { a.Finish("capture_time_limit", true) })
	t.current = a
	return a
}

type Attempt struct {
	manager       *Manager
	data          Capture
	bytes         int
	responseBytes int
	finished      bool
	timer         *time.Timer
}

// All payload mutations use the manager lock. No disk or network I/O occurs here.
func (a *Attempt) appendLocked(body []byte, request bool) {
	if a.finished || len(body) == 0 {
		return
	}
	limit := responseLimit - a.responseBytes
	if request {
		limit = requestLimit - len(a.data.RequestBody)
	}
	if available := memoryLimit - a.manager.memory; available < limit {
		limit = available
	}
	n := len(body)
	if n > limit {
		n = limit
		a.data.Truncated = true
		a.data.Reason = "size_limit"
	}
	if n < 0 {
		n = 0
	}
	if request {
		a.data.RequestBody = append(a.data.RequestBody, body[:n]...)
	} else {
		a.data.ResponseBody = append(a.data.ResponseBody, body[:n]...)
		a.responseBytes += n
	}
	a.bytes += n
	a.manager.memory += n
}

func (a *Attempt) Append(body []byte) {
	if a == nil {
		return
	}
	a.manager.mu.Lock()
	defer a.manager.mu.Unlock()
	a.appendLocked(body, false)
}

func (a *Attempt) Frame(body []byte) {
	if a == nil {
		return
	}
	m := a.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if a.finished {
		return
	}
	// Frame overhead is included so tiny-frame floods remain bounded.
	n := len(body)
	left := responseLimit - a.responseBytes
	if remaining := memoryLimit - m.memory; remaining < left {
		left = remaining
	}
	if n+64 > left {
		a.data.Truncated = true
		a.data.Reason = "size_limit"
		return
	}
	a.data.Frames = append(a.data.Frames, Frame{At: m.now(), Body: append([]byte(nil), body...)})
	a.bytes += n + 64
	a.responseBytes += n + 64
	m.memory += n + 64
}

func (a *Attempt) Headers(status int, headers http.Header, reused bool) {
	if a == nil {
		return
	}
	m := a.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if a.finished {
		return
	}
	if a.data.ResponseHeaders != nil {
		return
	}
	a.data.Status = status
	a.data.HandshakeReused = reused
	size := 0
	for k, vs := range headers {
		for _, v := range vs {
			size += len(k) + len(v) + 32
		}
	}
	if size > memoryLimit-m.memory || size > 256<<10 {
		a.data.Truncated = true
		a.data.Reason = "header_size_limit"
		return
	}
	a.data.ResponseHeaders = headers.Clone()
	a.bytes += size
	m.memory += size
}

func (a *Attempt) Finish(reason string, truncated bool) {
	if a == nil {
		return
	}
	m := a.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if a.finished {
		return
	}
	a.finished = true
	now := m.now()
	a.data.FinishedAt = &now
	if reason != "" && a.data.Reason == "" {
		a.data.Reason = reason
	}
	a.data.Truncated = a.data.Truncated || truncated
	if a.timer != nil {
		a.timer.Stop()
	}
	select {
	case m.queue <- a:
	default:
		delete(m.active, a.data.ID)
		m.memory -= a.bytes
		a.clearPayloadLocked()
		m.dropped++
	}
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	until := time.Unix(0, m.until.Load())
	return Status{Enabled: m.now().Before(until), Until: until, Captured: m.count, Dropped: m.dropped, Active: len(m.active), StorageErrors: m.storageErrors}
}

func (m *Manager) Disable() { m.until.Store(0) }

func (a *Attempt) clearPayloadLocked() {
	a.data.RequestBody = nil
	a.data.ResponseBody = nil
	a.data.Frames = nil
	a.data.ResponseHeaders = nil
	a.data.ResponseTrailers = nil
}
