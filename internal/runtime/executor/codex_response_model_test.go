package executor

import (
	"bytes"
	"context"
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

const codexResponseModelTestStream = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.6-luna"}}

data: {"type":"response.output_text.delta","delta":"he"}

data: {"type":"response.output_text.delta","delta":"llo"}

data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.6-luna","usage":{"input_tokens":5,"output_tokens":7,"total_tokens":12}}}

data: [DONE]`

// TestObserveCodexTokenEventRecordsResponseModel guards the wiring between the codex
// stream loops and the usage reporter: every transport funnels through observeCodexTokenEvent.
func TestObserveCodexTokenEventRecordsResponseModel(t *testing.T) {
	reporter := helps.NewExecutorUsageReporter(context.Background(), NewCodexExecutor(&config.Config{}), "gpt-6-astra", nil)

	for _, line := range bytes.Split([]byte(codexResponseModelTestStream), []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, dataTag) {
			continue
		}
		observeCodexTokenEvent(reporter, bytes.TrimSpace(line[len(dataTag):]))
	}

	if got := reporter.ResponseModel(); got != "gpt-5.6-luna" {
		t.Fatalf("reporter response model = %q, want %q", got, "gpt-5.6-luna")
	}
}

type codexResponseModelUsageCapture struct {
	alias   string
	records chan usage.Record
}

func (c *codexResponseModelUsageCapture) HandleUsage(_ context.Context, record usage.Record) {
	if record.Alias != c.alias {
		return
	}
	select {
	case c.records <- record:
	default:
	}
}

type codexResponseModelNoopUsagePlugin struct{}

func (codexResponseModelNoopUsagePlugin) HandleUsage(context.Context, usage.Record) {}

func (c *codexResponseModelUsageCapture) await(t *testing.T) usage.Record {
	t.Helper()
	select {
	case record := <-c.records:
		return record
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the usage record")
		return usage.Record{}
	}
}

// TestCodexUsageRecordsCarryResponseModelPerModel checks that the attempt record reports
// the served model while the image generation tool record must not claim it.
func TestCodexUsageRecordsCarryResponseModelPerModel(t *testing.T) {
	const alias = "codex-response-model-wiring-test"
	capture := &codexResponseModelUsageCapture{alias: alias, records: make(chan usage.Record, 4)}
	usage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin(t.Name(), codexResponseModelNoopUsagePlugin{})
	})

	ctx := usage.WithRequestedModelAlias(context.Background(), alias)
	auth := &cliproxyauth.Auth{ID: "codex-auth-1", Index: "auth-index-7", Provider: "codex"}
	reporter := helps.NewExecutorUsageReporter(ctx, NewCodexExecutor(&config.Config{}), "gpt-5.4-mini", auth)

	observeCodexTokenEvent(reporter, []byte(`{"type":"response.completed","response":{"model":"gpt-5.4-mini","usage":{"total_tokens":12}}}`))
	reporter.EnsurePublished(ctx)
	reporter.PublishAdditionalModel(ctx, "gpt-image-1.5", usage.Detail{TotalTokens: 5})

	attemptRecord := capture.await(t)
	if attemptRecord.Model != "gpt-5.4-mini" {
		t.Fatalf("attempt record model = %q, want %q", attemptRecord.Model, "gpt-5.4-mini")
	}
	if attemptRecord.ResponseModel != "gpt-5.4-mini" {
		t.Fatalf("attempt record response model = %q, want %q", attemptRecord.ResponseModel, "gpt-5.4-mini")
	}

	imageRecord := capture.await(t)
	if imageRecord.Model != "gpt-image-1.5" {
		t.Fatalf("image record model = %q, want %q", imageRecord.Model, "gpt-image-1.5")
	}
	if imageRecord.ResponseModel != "" {
		t.Fatalf("image record response model = %q, want empty", imageRecord.ResponseModel)
	}
}
