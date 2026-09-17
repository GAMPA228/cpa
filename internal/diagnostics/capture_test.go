package diagnostics

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testManager(t *testing.T) (*Manager, string) {
	t.Helper()
	m := NewManager()
	path := filepath.Join(t.TempDir(), "capture.sqlite3")
	if err := m.Open(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	if err := m.Enable(); err != nil {
		t.Fatal(err)
	}
	return m, path
}

func captures(t *testing.T, m *Manager, id string) []Capture {
	t.Helper()
	items, err := m.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestWindowRetryIsolationAndShutdown(t *testing.T) {
	m, path := testManager(t)
	ctx := m.Context(context.Background())
	other := m.Context(context.Background())
	if ID(ctx) == ID(other) || ID(ctx) == "" {
		t.Fatal("trace IDs not isolated")
	}
	a := Begin(ctx, Metadata{URL: "https://user:secret@example.com/responses?token=secret", AuthID: "a"}, []byte("first"), false)
	b := Begin(ctx, Metadata{URL: "https://example.com/responses", AuthID: "b"}, []byte("retry"), false)
	if a == nil || b == nil {
		t.Fatal("missing attempts")
	}
	if err := m.Delete(ID(ctx)); err == nil {
		t.Fatal("deleted active request")
	}
	m.until.Store(time.Now().Add(-time.Second).UnixNano())
	if m.Status().Enabled || ID(m.Context(context.Background())) != "" {
		t.Fatal("capture window not expired")
	}
	b.Append([]byte("completion after window"))
	b.Finish("", false)
	if Begin(other, Metadata{}, nil, false) != nil {
		t.Fatal("accepted attempt after deadline")
	}
	m.Close()
	if err := m.Open(path); err != nil {
		t.Fatal(err)
	}
	items := captures(t, m, ID(ctx))
	if len(items) != 2 {
		t.Fatalf("attempt count %d", len(items))
	}
	byAuth := map[string]Capture{}
	for _, c := range items {
		byAuth[c.AuthID] = c
	}
	if byAuth["a"].URL != "https://example.com/responses" || byAuth["a"].Reason != "superseded" {
		t.Fatalf("first attempt %+v", byAuth["a"])
	}
	if string(byAuth["b"].ResponseBody) != "completion after window" || byAuth["b"].Truncated {
		t.Fatal("pending attempt did not finish")
	}
	if m.Status().Enabled {
		t.Fatal("capture enabled after restart")
	}
	if err := m.Delete(ID(ctx)); err != nil {
		t.Fatal(err)
	}
	if len(captures(t, m, ID(ctx))) != 0 {
		t.Fatal("delete failed")
	}
}

func TestHTTPPreservesBytesHeadersAndTrailers(t *testing.T) {
	m, _ := testManager(t)
	request := []byte("{\"input\":\"private test\"}")
	response := []byte("data: {\"type\":\"response.completed\"}\n\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || !bytes.Equal(body, request) {
			t.Error("request changed")
		}
		w.Header().Add("Set-Cookie", "a=1; HttpOnly")
		w.Header().Add("Set-Cookie", "b=2; Secure")
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Trailer", "X-Final")
		w.WriteHeader(429)
		_, _ = w.Write(response)
		w.Header().Set("X-Final", "complete")
	}))
	defer server.Close()
	ctx := m.Context(context.Background())
	Begin(ctx, Metadata{URL: server.URL, Method: "POST"}, []byte("outdated log body"), false)
	client := server.Client()
	WrapHTTP(ctx, client)
	req, _ := http.NewRequestWithContext(ctx, "POST", server.URL, bytes.NewReader(request))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 429 || !bytes.Equal(body, response) {
		t.Fatal("response changed")
	}
	items := captures(t, m, ID(ctx))
	if len(items) != 1 {
		t.Fatalf("captures: %d", len(items))
	}
	c := items[0]
	if !bytes.Equal(c.RequestBody, request) || !bytes.Equal(c.ResponseBody, response) || c.Truncated {
		t.Fatalf("body mismatch: %+v", c)
	}
	if len(c.ResponseHeaders.Values("Set-Cookie")) != 2 || c.ResponseTrailers.Get("X-Final") != "complete" {
		t.Fatalf("headers lost: %+v", c)
	}
}

func TestLimitsAndConcurrentFrames(t *testing.T) {
	m, _ := testManager(t)
	ctx := m.Context(context.Background())
	a := Begin(ctx, Metadata{}, bytes.Repeat([]byte("a"), requestLimit+1), true)
	a.Headers(101, http.Header{"Set-Cookie": {"a", "b"}}, false)
	a.Headers(101, http.Header{"X-Other": {"ignored"}}, true)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); a.Frame([]byte("{\"raw\":true}")) }()
	}
	wg.Wait()
	items := captures(t, m, ID(ctx))
	if len(items) != 1 || len(items[0].Frames) != 20 || !items[0].Truncated || len(items[0].RequestBody) != requestLimit || items[0].HandshakeReused {
		t.Fatal("limits or handshake preservation failed")
	}
	a.Finish("", false)
	m.Disable()
	if ID(m.Context(context.Background())) != "" {
		t.Fatal("disable failed")
	}
}

func TestRetentionAndSizeBound(t *testing.T) {
	m, path := testManager(t)
	ctx := m.Context(context.Background())
	a := Begin(ctx, Metadata{}, nil, false)
	a.Append(bytes.Repeat([]byte("x"), responseLimit+100))
	if c := captures(t, m, ID(ctx))[0]; !c.Truncated || len(c.ResponseBody) != responseLimit {
		t.Fatal("response limit not enforced")
	}
	a.Finish("", false)
	m.Close()
	m.now = func() time.Time { return time.Now().Add(Retention + time.Minute) }
	if err := m.Open(path); err != nil {
		t.Fatal(err)
	}
	if len(captures(t, m, ID(ctx))) != 0 {
		t.Fatal("expired capture visible")
	}
	m.cleanup()
	var count int
	if err := m.store.db.QueryRow("SELECT count(*) FROM captures").Scan(&count); err != nil || count != 0 {
		t.Fatalf("cleanup count=%d err=%v", count, err)
	}
}
