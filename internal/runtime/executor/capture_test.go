package executor

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/diagnostics"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
)

func TestCaptureRawWebsocketFramesAndReusedHandshake(t *testing.T) {
	m := diagnostics.NewManager()
	if err := m.Open(filepath.Join(t.TempDir(), "capture.sqlite3")); err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.Enable(); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"type":"response.completed","response":{"id":"upstream-identity","service_tier":"default"}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, http.Header{"X-Upstream-Test": {"a", "b"}})
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_ = conn.WriteMessage(websocket.TextMessage, payload)
	}))
	defer server.Close()
	conn, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx := m.Context(context.Background())
	helps.RecordAPIWebsocketRequest(ctx, &config.Config{}, helps.UpstreamRequestLog{URL: server.URL, Body: []byte(`{"input":"test"}`)})
	_, got, err := readCodexWebsocketMessage(ctx, nil, conn, nil)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatal("websocket response changed")
	}
	items, err := m.Get(diagnostics.ID(ctx))
	if err != nil || len(items) != 1 || len(items[0].Frames) != 1 || !bytes.Equal(items[0].Frames[0].Body, payload) {
		t.Fatalf("raw frame missing: %+v %v", items, err)
	}
	ctx = m.Context(context.Background())
	helps.RecordAPIWebsocketRequest(ctx, nil, helps.UpstreamRequestLog{URL: server.URL})
	sess := &codexWebsocketSession{captureHandshake: response.Header.Clone()}
	ch := make(chan codexWebsocketRead, 1)
	// The reader checks cancellation before waiting; handshake metadata is captured first.
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	_, _, _ = readCodexWebsocketMessage(cancelCtx, sess, conn, ch)
	items, err = m.Get(diagnostics.ID(ctx))
	if err != nil || len(items) != 1 || !items[0].HandshakeReused || len(items[0].ResponseHeaders.Values("X-Upstream-Test")) != 2 {
		t.Fatalf("reused handshake missing: %+v %v", items, err)
	}
}
