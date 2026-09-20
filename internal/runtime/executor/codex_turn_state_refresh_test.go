package executor

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/turnstate"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

const refreshTestTerminal = `{"type":"response.completed","response":{"id":"resp-refresh","object":"response","status":"completed","model":"other-model","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`

func refreshTestToken(issued time.Time) string {
	data := make([]byte, 217)
	data[0] = 0x80
	binary.BigEndian.PutUint64(data[1:9], uint64(issued.Unix()))
	return base64.URLEncoding.EncodeToString(data)
}

func refreshTestManager(t *testing.T, authID string) (*turnstate.Manager, string, func()) {
	t.Helper()
	previous := turnstate.Default
	manager := turnstate.NewManager()
	turnstate.Default = manager
	path := filepath.Join(t.TempDir(), "refresh.sqlite3")
	t.Cleanup(func() { manager.Close(); turnstate.Default = previous })
	if err := manager.Open(path); err != nil {
		t.Fatal(err)
	}
	if err := manager.Configure(turnstate.Settings{Enabled: true, MaxChars: 292}); err != nil {
		t.Fatal(err)
	}
	drain := func() {
		t.Helper()
		manager.Close()
		if err := manager.Open(path); err != nil {
			t.Fatal(err)
		}
	}
	stale := refreshTestToken(time.Now().Add(-56 * time.Minute))
	manager.Observe(authID, "gpt-5.4", stale)
	drain()
	if rule, ok := manager.Lookup(authID, "gpt-5.4"); !ok || rule.Value != stale {
		t.Fatal("stale rule was not seeded")
	}
	return manager, stale, drain
}

func refreshTestRequest(authID, url, stale string) (*cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) {
	auth := &cliproxyauth.Auth{ID: authID, Provider: "codex", Attributes: map[string]string{"base_url": url}, Metadata: map[string]any{"access_token": "test-token"}}
	req := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":[]}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Headers: http.Header{turnstate.Header: []string{stale}}, Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: authID}}
	return auth, req, opts
}

func refreshTestDrainStream(t *testing.T, result *cliproxyexecutor.StreamResult) {
	t.Helper()
	for {
		select {
		case chunk, ok := <-result.Chunks:
			if !ok {
				return
			}
			if chunk.Err != nil {
				t.Fatal(chunk.Err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("stream did not finish")
		}
	}
}

func refreshTestHeader(t *testing.T, headers <-chan string, want string) {
	t.Helper()
	select {
	case got := <-headers:
		if got != want {
			t.Fatalf("outgoing turn state = %q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("upstream request not received")
	}
}

func TestCodexTurnStateRefreshTransports(t *testing.T) {
	for _, transport := range []string{"http", "http-stream", "websocket", "websocket-stream"} {
		t.Run(transport, func(t *testing.T) {
			authID := t.Name()
			manager, stale, drain := refreshTestManager(t, authID)
			fresh := refreshTestToken(time.Now())
			headers := make(chan string, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				outgoing := r.Header.Get(turnstate.Header)
				headers <- outgoing
				responseHeaders := make(http.Header)
				if outgoing == "" {
					responseHeaders.Set(turnstate.Header, fresh)
				}
				if strings.HasPrefix(transport, "http") {
					if outgoing == "" {
						w.Header().Set(turnstate.Header, fresh)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: %s\n\n", refreshTestTerminal)
					return
				}
				upgrader := websocket.Upgrader{}
				conn, err := upgrader.Upgrade(w, r, responseHeaders)
				if err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = conn.Close() }()
				for {
					if _, _, err := conn.ReadMessage(); err != nil {
						return
					}
					if err := conn.WriteMessage(websocket.TextMessage, []byte(refreshTestTerminal)); err != nil {
						return
					}
				}
			}))
			defer server.Close()
			cfg := &config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}}
			httpExec := NewCodexExecutor(cfg)
			wsExec := NewCodexWebsocketsExecutor(cfg)
			defer wsExec.CloseExecutionSession(authID)
			auth, req, opts := refreshTestRequest(authID, server.URL, stale)
			execute := func() {
				t.Helper()
				var err error
				var result *cliproxyexecutor.StreamResult
				switch transport {
				case "http":
					_, err = httpExec.Execute(context.Background(), auth, req, opts)
				case "http-stream":
					result, err = httpExec.ExecuteStream(context.Background(), auth, req, opts)
				case "websocket":
					_, err = wsExec.Execute(context.Background(), auth, req, opts)
				case "websocket-stream":
					result, err = wsExec.ExecuteStream(context.Background(), auth, req, opts)
				}
				if err != nil {
					t.Fatal(err)
				}
				if result != nil {
					refreshTestDrainStream(t, result)
				}
			}
			execute()
			refreshTestHeader(t, headers, "")
			drain()
			if rule, ok := manager.Lookup(authID, req.Model); !ok || rule.Value != fresh {
				t.Fatal("fresh token was not persisted for selected account/model")
			}
			if _, ok := manager.Lookup(authID, "other-model"); ok {
				t.Fatal("response model incorrectly used for renewal")
			}
			execute()
			refreshTestHeader(t, headers, fresh)
			drain()
		})
	}
}

func TestCodexTurnStateRefreshWebsocketStreamReservation(t *testing.T) {
	for _, responseHeader := range []string{"", "invalid-turn-state"} {
		t.Run("header="+responseHeader, func(t *testing.T) {
			authID := t.Name()
			manager, stale, drain := refreshTestManager(t, authID)
			headers := make(chan string, 4)
			finish := make(chan struct{})
			var finishOnce sync.Once
			release := func() { finishOnce.Do(func() { close(finish) }) }
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				first := requests.Add(1) == 1
				headers <- r.Header.Get(turnstate.Header)
				responseHeaders := make(http.Header)
				if responseHeader != "" {
					responseHeaders.Set(turnstate.Header, responseHeader)
				}
				upgrader := websocket.Upgrader{}
				conn, err := upgrader.Upgrade(w, r, responseHeaders)
				if err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = conn.Close() }()
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
				if first {
					<-finish
				}
				_ = conn.WriteMessage(websocket.TextMessage, []byte(refreshTestTerminal))
				_, _, _ = conn.ReadMessage()
			}))
			defer server.Close()
			defer release()
			exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
			defer exec.CloseExecutionSession(authID)
			defer exec.CloseExecutionSession(authID + "-second")
			auth, req, opts := refreshTestRequest(authID, server.URL, stale)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			first, err := exec.ExecuteStream(ctx, auth, req, opts)
			if err != nil {
				t.Fatal(err)
			}
			refreshTestHeader(t, headers, "")
			opts.Metadata = map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: authID + "-second"}
			second, err := exec.ExecuteStream(ctx, auth, req, opts)
			if err != nil {
				t.Fatal(err)
			}
			refreshTestDrainStream(t, second)
			refreshTestHeader(t, headers, stale)
			release()
			refreshTestDrainStream(t, first)
			drain()
			if rule, ok := manager.Lookup(authID, req.Model); !ok || rule.Value != stale {
				t.Fatal("missing or invalid response header replaced the old token")
			}
		})
	}
}

func TestCodexTurnStateRefreshReplayRequiredRelease(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			authID := t.Name()
			manager, stale, _ := refreshTestManager(t, authID)
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()
			exec := NewCodexWebsocketsExecutor(&config.Config{})
			defer exec.CloseExecutionSession(authID)
			auth, req, opts := refreshTestRequest(authID, server.URL, stale)
			ctx := cliproxyexecutor.WithRequiredUpstreamWebsocket(context.Background())
			var err error
			if stream {
				_, err = exec.ExecuteStream(ctx, auth, req, opts)
			} else {
				_, err = exec.Execute(ctx, auth, req, opts)
			}
			if !cliproxyexecutor.IsUpstreamWebsocketReplayRequired(err) {
				t.Fatalf("error = %v, want replay required", err)
			}
			if requests.Load() != 0 {
				t.Fatal("replay-required request dialed upstream or fell back to HTTP")
			}
			value, omit, refresh := manager.PrepareRequest(authID, req.Model)
			if refresh != nil {
				defer refresh.Finish(false)
			}
			if value != "" || !omit || refresh == nil {
				t.Fatal("replay-required request leaked its reservation or started cooldown")
			}
		})
	}
}
