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
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/authheaders"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/turnstate"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestCodexTurnStateRealRequests(t *testing.T) {
	for _, transport := range []string{"http", "http-stream", "websocket", "websocket-stream"} {
		t.Run(transport, func(t *testing.T) {
			old := turnstate.Default
			manager := turnstate.NewManager()
			turnstate.Default = manager
			defer func() { manager.Close(); turnstate.Default = old }()
			path := filepath.Join(t.TempDir(), "state.sqlite3")
			if err := manager.Open(path); err != nil {
				t.Fatal(err)
			}
			if err := manager.Configure(turnstate.Settings{Enabled: true, MaxChars: 292}); err != nil {
				t.Fatal(err)
			}
			data := make([]byte, 217)
			data[0] = 0x80
			binary.BigEndian.PutUint64(data[1:9], uint64(time.Now().Unix()))
			token := base64.URLEncoding.EncodeToString(data)
			var handshakes atomic.Int32
			headers := make(chan string, 8)
			terminal := `{"type":"response.completed","response":{"id":"resp-ts","object":"response","status":"completed","model":"upstream-different-model","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				headers <- r.Header.Get(turnstate.Header)
				if strings.HasPrefix(transport, "http") {
					w.Header().Set(turnstate.Header, token)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: %s\n\n", terminal)
					return
				}
				upgrader := websocket.Upgrader{}
				conn, err := upgrader.Upgrade(w, r, http.Header{turnstate.Header: []string{token}})
				if err != nil {
					t.Error(err)
					return
				}
				handshakes.Add(1)
				defer func() { _ = conn.Close() }()
				for {
					if _, _, err = conn.ReadMessage(); err != nil {
						return
					}
					if err = conn.WriteMessage(websocket.TextMessage, []byte(terminal)); err != nil {
						return
					}
				}
			}))
			defer server.Close()
			authID := "turn-state-" + t.Name()
			sink := &responseModelUsageSink{authID: authID, records: make(chan usage.Record, 8)}
			usage.RegisterPlugin(sink)
			cfg := &config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}}
			httpExec := NewCodexExecutor(cfg)
			wsExec := NewCodexWebsocketsExecutor(cfg)
			defer wsExec.CloseExecutionSession(authID)
			auth := &cliproxyauth.Auth{ID: authID, Provider: "codex", Attributes: map[string]string{"base_url": server.URL}, Metadata: map[string]any{"access_token": "test-token"}}
			req := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":[]}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: authID}}
			opts.CodexHeaderHost = &turnStateTestHost{manager: manager}
			execute := func() usage.Record {
				t.Helper()
				var err error
				if strings.HasSuffix(transport, "-stream") {
					var result *cliproxyexecutor.StreamResult
					if transport == "http-stream" {
						result, err = httpExec.ExecuteStream(context.Background(), auth, req, opts)
					} else {
						result, err = wsExec.ExecuteStream(context.Background(), auth, req, opts)
					}
					if err == nil {
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								err = chunk.Err
							}
						}
					}
				} else if transport == "http" {
					_, err = httpExec.Execute(context.Background(), auth, req, opts)
				} else {
					_, err = wsExec.Execute(context.Background(), auth, req, opts)
				}
				if err != nil {
					t.Fatal(err)
				}
				select {
				case record := <-sink.records:
					return record
				case <-time.After(5 * time.Second):
					t.Fatal("usage not published")
					return usage.Record{}
				}
			}
			first := execute()
			if first.TurnStateLength == nil || *first.TurnStateLength != 292 {
				t.Fatal("actual response length missing")
			}
			if got := <-headers; got != "" {
				t.Fatal("unexpected header before first observation")
			}
			// Drain persistence deterministically and prove restart does not need the admin page.
			manager.Close()
			if err := manager.Open(path); err != nil {
				t.Fatal(err)
			}
			if _, ok := manager.Lookup(authID, "gpt-5.4"); !ok {
				t.Fatal("rule was not created for actual request model")
			}
			if _, ok := manager.Lookup(authID, "upstream-different-model"); ok {
				t.Fatal("response model incorrectly used for matching")
			}
			second := execute()
			if second.TurnStateLength == nil || *second.TurnStateLength != 292 {
				t.Fatal("new response length missing")
			}
			if got := <-headers; got != token {
				t.Fatal("automatic rule was not applied to next request")
			}
			if strings.HasPrefix(transport, "websocket") {
				third := execute()
				if third.TurnStateLength != nil {
					t.Fatal("reused handshake falsely reported as fresh response header")
				}
				if handshakes.Load() != 2 {
					t.Fatalf("handshake count %d, want 2", handshakes.Load())
				}
			}
			if err := manager.Configure(turnstate.Settings{Enabled: true, MaxChars: 292, AccountScope: "selected", AuthIDs: []string{"other-account"}}); err != nil {
				t.Fatal(err)
			}
			excluded := execute()
			if excluded.TurnStateLength == nil || *excluded.TurnStateLength != 292 {
				t.Fatal("account exclusion stopped length statistics")
			}
			if got := <-headers; got != "" {
				t.Fatal("excluded account reused automatic request header")
			}
			if strings.HasPrefix(transport, "websocket") && handshakes.Load() != 3 {
				t.Fatal("account exclusion did not replace old WebSocket handshake")
			}
			if err := manager.Configure(turnstate.Settings{Enabled: true, MaxChars: 292}); err != nil {
				t.Fatal(err)
			}
			execute()
			if got := <-headers; got != token {
				t.Fatal("re-enrollment did not restore valid rule")
			}
			if err := manager.Configure(turnstate.Settings{Enabled: false, MaxChars: 292}); err != nil {
				t.Fatal(err)
			}
			last := execute()
			if last.TurnStateLength == nil || *last.TurnStateLength != 292 {
				t.Fatal("disabled automation stopped length statistics")
			}
			if got := <-headers; got != "" {
				t.Fatal("disabled automation still injected header")
			}
		})
	}
}

func TestCodexTurnStatePluginDisabledOrUnloadedRealRequests(t *testing.T) {
	for _, mode := range []string{"disabled", "unloaded"} {
		for _, transport := range []string{"http", "http-stream", "websocket", "websocket-stream"} {
			for _, source := range []string{"headers", "metadata"} {
				if source == "metadata" && strings.HasPrefix(transport, "http") {
					continue
				}
				t.Run(mode+"/"+transport+"/"+source, func(t *testing.T) {
					authID := t.Name()
					manager, stale, drain := refreshTestManager(t, authID)
					fresh := refreshTestToken(time.Now())
					requests := make(chan http.Header, 4)
					metadata := fmt.Sprintf(`{"type":"codex.response.metadata","headers":{"x-codex-turn-state":%q}}`, fresh)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests <- r.Header.Clone()
						responseHeaders := http.Header{"Set-Cookie": {"native_session=must-not-replay; Path=/"}}
						if source == "headers" {
							responseHeaders.Set(turnstate.Header, fresh)
						}
						if strings.HasPrefix(transport, "http") {
							for name, values := range responseHeaders {
								w.Header()[name] = values
							}
							w.Header().Set("Content-Type", "text/event-stream")
							if source == "metadata" {
								_, _ = fmt.Fprintf(w, "data: %s\n\n", metadata)
							}
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
							if source == "metadata" {
								if err := conn.WriteMessage(websocket.TextMessage, []byte(metadata)); err != nil {
									return
								}
							}
							if err := conn.WriteMessage(websocket.TextMessage, []byte(refreshTestTerminal)); err != nil {
								return
							}
						}
					}))
					defer server.Close()
					cfg := &config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}, CodexHeaderDefaults: config.CodexHeaderDefaults{UserAgent: "global-agent"}}
					httpExec := NewCodexExecutor(cfg)
					wsExec := NewCodexWebsocketsExecutor(cfg)
					defer wsExec.CloseExecutionSession(authID)
					auth, req, opts := refreshTestRequest(authID, server.URL, "")
					auth.Metadata[authheaders.MetadataKey] = []authheaders.Rule{
						{Name: "User-Agent", Operation: "override", Value: "native-agent"},
						{Name: "Cookie", Operation: "override", Value: "native=forbidden"},
					}
					sink := &responseModelUsageSink{authID: authID, records: make(chan usage.Record, 4)}
					usage.RegisterPlugin(sink)
					host := &testCodexHeaderHost{response: pluginapi.CodexHeaderResponse{
						Signature: "plugin-active", Headers: http.Header{"Cookie": {"plugin=only"}, turnstate.Header: {"plugin-state"}},
					}}
					opts.CodexHeaderHost = host
					for attempt := 0; attempt < 3; attempt++ {
						if attempt == 1 {
							host = &testCodexHeaderHost{disabled: true}
							opts.CodexHeaderHost = nil
							if mode == "disabled" {
								opts.CodexHeaderHost = host
							}
						}
						if attempt == 2 {
							// A fresh handshake also must not recover a native cookie jar.
							wsExec.CloseExecutionSession(authID)
						}
						var result *cliproxyexecutor.StreamResult
						var err error
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
						select {
						case got := <-requests:
							if attempt == 0 {
								if got.Get("Cookie") != "plugin=only" || got.Get(turnstate.Header) != "plugin-state" {
									t.Fatalf("plugin headers not sent: %v", got)
								}
							} else if got.Get("Cookie") != "" || got.Get(turnstate.Header) != "" || got.Get("User-Agent") != "global-agent" {
								t.Fatalf("absent plugin retained or applied native headers: %v", got)
							}
						default:
							t.Fatal("disabled/unloaded signature reused the plugin WebSocket handshake")
						}
						select {
						case record := <-sink.records:
							if record.TurnStateLength == nil || *record.TurnStateLength != len(fresh) {
								t.Fatalf("response length not recorded: %v", record.TurnStateLength)
							}
						case <-time.After(5 * time.Second):
							t.Fatal("usage not published")
						}
					}
					drain()
					if rule, ok := manager.Lookup(authID, req.Model); !ok || rule.Value != stale {
						t.Fatal("response observed into native turn-state storage")
					}
					if len(manager.Rules(authID)) != 1 {
						t.Fatal("response created a native automatic rule")
					}
					if host.prepared.AuthID != "" || host.observed.AuthID != "" || host.completed.ReservationID != "" {
						t.Fatal("disabled host invoked")
					}
				})
			}
		}
	}
}
