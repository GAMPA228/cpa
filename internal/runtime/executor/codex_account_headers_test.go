package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/authheaders"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestCodexAccountHeadersRealRequests(t *testing.T) {
	for _, transport := range []string{"http", "websocket", "http-stream", "websocket-stream"} {
		t.Run(transport, func(t *testing.T) {
			headers := make(chan http.Header, 8)
			upgrader := websocket.Upgrader{}
			const completed = `{"type":"response.completed","response":{"id":"resp-test","object":"response","status":"completed","model":"gpt-5.4","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				headers <- r.Header.Clone()
				if strings.HasPrefix(transport, "http") {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: %s\n\n", completed)
					return
				}
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = conn.Close() }()
				for {
					if _, _, err := conn.ReadMessage(); err != nil {
						return
					}
					if err := conn.WriteMessage(websocket.TextMessage, []byte(completed)); err != nil {
						return
					}
				}
			}))
			defer server.Close()
			cfg := &config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}, CodexHeaderDefaults: config.CodexHeaderDefaults{UserAgent: "global-agent", Version: "global-version"}}
			httpExec := NewCodexExecutor(cfg)
			wsExec := NewCodexWebsocketsExecutor(cfg)
			defer wsExec.CloseExecutionSession("account-headers-test")
			auth := &cliproxyauth.Auth{ID: "account-a", Provider: "codex", Attributes: map[string]string{"base_url": server.URL}, Metadata: map[string]any{"access_token": "test-token", "account_id": "account-a"}}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Headers: http.Header{"X-Openai-Internal-Codex-Responses-Lite": {"true"}}, Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "account-headers-test"}}
			req := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":[]}`)}
			host := &testCodexHeaderHost{}
			opts.CodexHeaderHost = host
			for i, want := range []string{"account-agent-a", "account-agent-b", "global-agent", "global-agent"} {
				if i > 0 {
					req.Model = "gpt-5.5"
					req.Payload = []byte(`{"model":"gpt-5.5","input":[]}`)
				}
				if i < 3 {
					auth.Metadata[authheaders.MetadataKey] = []authheaders.Rule{
						{Name: "User-Agent", Operation: "override", Value: "account-agent-a", Models: []string{"gpt-5.4"}},
						{Name: "User-Agent", Operation: "override", Value: "account-agent-b", Models: []string{"gpt-5.5"}},
						{Name: "Version", Operation: "default", Value: "ignored"},
						{Name: "X-Account-Test", Operation: "default", Value: "added"},
						{Name: "X-OpenAI-Internal-Codex-Responses-Lite", Operation: "delete"},
					}
				} else {
					delete(auth.Metadata, authheaders.MetadataKey)
				}
				host.response = pluginapi.CodexHeaderResponse{Headers: http.Header{"User-Agent": {want}}, Signature: fmt.Sprintf("plugin-version-%d", i)}
				if i < 3 {
					host.response.Headers.Set("X-Account-Test", "added")
					host.response.ClearHeaders = []string{"X-OpenAI-Internal-Codex-Responses-Lite"}
				}
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
								t.Fatal(chunk.Err)
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
				wantRules, err := json.Marshal(auth.Metadata[authheaders.MetadataKey])
				if err != nil {
					t.Fatal(err)
				}
				if host.prepared.AuthID != auth.ID || host.prepared.Model != req.Model || string(host.prepared.Rules) != string(wantRules) {
					t.Fatalf("selected account/model/rules not passed to plugin: %#v", host.prepared)
				}
				var got http.Header
				select {
				case got = <-headers:
				default:
					t.Fatal("reused a websocket with stale account headers")
				}
				if got.Get("User-Agent") != want || got.Get("Version") != "global-version" {
					t.Fatalf("wrong identity headers: %v", got)
				}
				if got.Get("Authorization") != "Bearer test-token" || got.Get("Chatgpt-Account-Id") != "account-a" {
					t.Fatal("credential headers changed")
				}
				if i < 3 && (got.Get("X-Account-Test") != "added" || got.Get("X-OpenAI-Internal-Codex-Responses-Lite") != "") {
					t.Fatal("plugin header additions/deletions not applied")
				}
				if i == 3 && got.Get("X-Account-Test") != "" {
					t.Fatal("removed rules still applied")
				}
			}
		})
	}
}

func TestCodexAccountHeadersSessionReuse(t *testing.T) {
	conn := &websocket.Conn{}
	sess := &codexWebsocketSession{conn: conn, connCloser: newWebsocketConnectionCloser(conn), authID: "a", wsURL: "ws://test", headerRulesKey: "before"}
	sess.resetUpstreamDisconnectError(conn)
	if got, _ := existingWebsocketSessionConn(sess, "a", "ws://test", "", "before"); got != conn {
		t.Fatal("unchanged rules prevent reuse")
	}
	if got, _ := existingWebsocketSessionConn(sess, "a", "ws://test", "", "after"); got != nil {
		t.Fatal("changed rules reused old session")
	}
	if got, _, _, _, _ := detachMismatchedWebsocketSessionConn(sess, "a", "ws://test", "", "after"); got != conn || sess.conn != nil {
		t.Fatal("old connection not detached")
	}
}
