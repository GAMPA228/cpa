package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type responseModelUsageSink struct {
	records chan usage.Record
	authID  string
}

func (s *responseModelUsageSink) HandleUsage(_ context.Context, r usage.Record) {
	if r.AuthID == s.authID {
		select {
		case s.records <- r:
		default:
		}
	}
}

func TestCodexResponseModelRealRequests(t *testing.T) {
	for _, transport := range []string{"http", "http-stream", "websocket", "websocket-stream"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/failure=%t", transport, fail), func(t *testing.T) {
				created := `{"type":"response.created","response":{"id":"resp-model","model":"upstream-early","status":"in_progress"}}`
				terminal := `{"type":"response.completed","response":{"id":"resp-model","object":"response","status":"completed","model":"upstream-final","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`
				if fail {
					terminal = `{"type":"response.failed","response":{"id":"resp-model","status":"failed","error":{"code":"invalid_request_error","message":"test failure"}}}`
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasPrefix(transport, "http") {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = fmt.Fprintf(w, "data: %s\n\ndata: %s\n\n", created, terminal)
						return
					}
					upgrader := websocket.Upgrader{}
					conn, err := upgrader.Upgrade(w, r, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer func() { _ = conn.Close() }()
					if _, _, err = conn.ReadMessage(); err != nil {
						return
					}
					_ = conn.WriteMessage(websocket.TextMessage, []byte(created))
					_ = conn.WriteMessage(websocket.TextMessage, []byte(terminal))
				}))
				defer server.Close()
				authID := "response-model-" + t.Name()
				sink := &responseModelUsageSink{authID: authID, records: make(chan usage.Record, 4)}
				usage.RegisterPlugin(sink)
				cfg := &config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}}
				httpExec := NewCodexExecutor(cfg)
				wsExec := NewCodexWebsocketsExecutor(cfg)
				defer wsExec.CloseExecutionSession(authID)
				auth := &cliproxyauth.Auth{ID: authID, Provider: "codex", Attributes: map[string]string{"base_url": server.URL}, Metadata: map[string]any{"access_token": "test-token"}}
				req := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":[]}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: authID}}
				var err error
				if strings.HasSuffix(transport, "-stream") {
					var result *cliproxyexecutor.StreamResult
					if strings.HasPrefix(transport, "http") {
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
				if !fail && err != nil {
					t.Fatal(err)
				}
				select {
				case record := <-sink.records:
					want := "upstream-final"
					if fail {
						want = "upstream-early"
					}
					if record.ResponseModel != want || record.Detail.ResponseModel != want {
						t.Fatalf("response model = %q / %q, want %q", record.ResponseModel, record.Detail.ResponseModel, want)
					}
					if record.Model != "gpt-5.4" {
						t.Fatalf("existing model changed: %q", record.Model)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("usage not published")
				}
			})
		}
	}
}
