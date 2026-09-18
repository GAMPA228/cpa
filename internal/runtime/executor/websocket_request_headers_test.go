package executor

import (
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

func TestWebsocketRequestHeadersActualHandshakeAndReuse(t *testing.T) {
	for _, provider := range []string{"codex", "xai"} {
		for _, captureFirst := range []bool{false, true} {
			name := provider + "/capture-after-connect"
			if captureFirst {
				name = provider + "/capture-before-connect"
			}
			t.Run(name, func(t *testing.T) {
				m := diagnostics.NewManager()
				if err := m.Open(filepath.Join(t.TempDir(), "capture.sqlite3")); err != nil {
					t.Fatal(err)
				}
				defer m.Close()
				if captureFirst {
					if err := m.Enable(); err != nil {
						t.Fatal(err)
					}
				}
				observed := make(chan http.Header, 4)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					headers := r.Header.Clone()
					headers.Set("Host", r.Host)
					observed <- headers
					upgrader := websocket.Upgrader{}
					conn, err := upgrader.Upgrade(w, r, http.Header{"X-Handshake": {"original"}})
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.Close()
					for {
						if _, _, errRead := conn.ReadMessage(); errRead != nil {
							return
						}
					}
				}))
				defer server.Close()
				wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
				sess := &codexWebsocketSession{sessionID: "capture-header-test"}
				defer closeCodexWebsocketSession(sess, "test_complete")
				codex := NewCodexWebsocketsExecutor(&config.Config{})
				xai := NewXAIWebsocketsExecutor(&config.Config{})
				connect := func(ctx context.Context, authID string, headers http.Header) (*websocket.Conn, *http.Response, error) {
					if provider == "xai" {
						conn, _, resp, err := xai.ensureUpstreamConn(ctx, nil, sess, authID, wsURL, headers)
						return conn, resp, err
					}
					conn, _, resp, err := codex.ensureUpstreamConn(ctx, nil, sess, authID, wsURL, headers)
					return conn, resp, err
				}
				headers := http.Header{"Authorization": {"Bearer private"}, "Cookie": {"private-cookie"}, "X-Api-Key": {"private-key"}, "X-Turn": {"first"}, "Host": {"virtual.example"}}
				ctx := m.Context(context.Background())
				helps.RecordAPIWebsocketRequest(ctx, nil, helps.UpstreamRequestLog{URL: wsURL})
				conn, resp, err := connect(ctx, "auth-a", headers)
				if err != nil {
					t.Fatal(err)
				}
				recordAPIWebsocketHandshake(ctx, nil, resp)
				diagnostics.Current(ctx).Finish("", false)
				actual := <-observed
				assertHeaders := func(ctx context.Context, reused bool, expected http.Header) {
					t.Helper()
					items, errGet := m.Get(diagnostics.ID(ctx))
					if errGet != nil || len(items) != 1 {
						t.Fatalf("captures: %v, %v", items, errGet)
					}
					got := items[0]
					for _, key := range []string{"Host", "X-Turn", "Upgrade", "Connection", "Sec-WebSocket-Key", "Sec-WebSocket-Version", "Sec-WebSocket-Extensions"} {
						if got.RequestHeaders.Get(key) != expected.Get(key) || expected.Get(key) == "" {
							t.Errorf("%s mismatch: captured %q, actual %q", key, got.RequestHeaders.Get(key), expected.Get(key))
						}
					}
					for _, key := range []string{"Authorization", "Cookie", "X-Api-Key"} {
						if got.RequestHeaders.Get(key) != "[REDACTED]" {
							t.Errorf("%s was not redacted", key)
						}
					}
					if got.HandshakeReused != reused || got.ResponseHeaders.Get("X-Handshake") != "original" {
						t.Errorf("unexpected handshake metadata: %+v", got)
					}
				}
				if captureFirst {
					assertHeaders(ctx, false, actual)
				}
				if err := m.Enable(); err != nil {
					t.Fatal(err)
				}
				headers.Set("X-Turn", "unsent-second-turn")
				headers.Set("Host", "unsent.example")
				ctx = m.Context(context.Background())
				helps.RecordAPIWebsocketRequest(ctx, nil, helps.UpstreamRequestLog{URL: wsURL, Headers: headers})
				reused, resp, err := connect(ctx, "auth-a", headers)
				if err != nil || resp != nil || reused != conn {
					t.Fatalf("connection not reused: %v", err)
				}
				diagnostics.Current(ctx).Finish("", false)
				assertHeaders(ctx, true, actual)
				ctx = m.Context(context.Background())
				helps.RecordAPIWebsocketRequest(ctx, nil, helps.UpstreamRequestLog{URL: wsURL, Headers: headers})
				_, resp, err = connect(ctx, "auth-b", headers)
				if err != nil || resp == nil {
					t.Fatalf("replacement connection failed: %v", err)
				}
				recordAPIWebsocketHandshake(ctx, nil, resp)
				diagnostics.Current(ctx).Finish("", false)
				assertHeaders(ctx, false, <-observed)
			})
		}
	}
}

func TestWebsocketRejectedHandshakeRequestHeaders(t *testing.T) {
	m := diagnostics.NewManager()
	if err := m.Open(filepath.Join(t.TempDir(), "capture.sqlite3")); err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.Enable(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	ctx := m.Context(context.Background())
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	helps.RecordAPIWebsocketRequest(ctx, nil, helps.UpstreamRequestLog{URL: wsURL})
	exec := NewCodexWebsocketsExecutor(&config.Config{})
	_, _, resp, err := exec.dialCodexWebsocket(ctx, nil, wsURL, http.Header{"Authorization": {"secret"}})
	if err == nil || resp == nil {
		t.Fatal("expected a rejected handshake")
	}
	helps.RecordAPIWebsocketUpgradeRejection(ctx, nil, helps.UpstreamRequestLog{URL: wsURL}, resp.StatusCode, resp.Header, websocketHandshakeBody(resp))
	items, err := m.Get(diagnostics.ID(ctx))
	if err != nil || len(items) != 1 || items[0].RequestHeaders.Get("Sec-WebSocket-Key") == "" || items[0].RequestHeaders.Get("Authorization") != "[REDACTED]" {
		t.Fatalf("rejected handshake headers missing: %v, %v", items, err)
	}
}
