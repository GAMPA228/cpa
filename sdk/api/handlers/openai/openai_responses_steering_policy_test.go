package openai

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func TestResponsesWebsocketResponseSteeringPolicyGate(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*config.SDKConfig)
		want      bool
	}{
		{name: "plain steering", want: true},
		{name: "flag disabled", configure: func(c *config.SDKConfig) { c.CodexResponseSteering = false }},
		{name: "rewrite enabled", configure: func(c *config.SDKConfig) {
			c.ModelRewrite = config.ModelRewriteConfig{Enabled: true, Rules: []config.ModelRewriteRule{{MatchModels: []string{"public"}, TargetModel: "private"}}}
		}},
		{name: "rewrite disabled", want: true, configure: func(c *config.SDKConfig) {
			c.ModelRewrite.Rules = []config.ModelRewriteRule{{MatchModels: []string{"public"}, TargetModel: "private"}}
		}},
		{name: "rewrite without rules", want: true, configure: func(c *config.SDKConfig) { c.ModelRewrite.Enabled = true }},
		{name: "daily quota", configure: func(c *config.SDKConfig) {
			c.APIKeyEntries = config.APIKeyEntryList{{APIKey: "key", DailyTokenLimit: 1}}
		}},
		{name: "unlimited keys", want: true, configure: func(c *config.SDKConfig) {
			c.APIKeyEntries = config.APIKeyEntryList{{APIKey: "key"}, {APIKey: "other", DailyTokenLimit: -1}}
		}},
		{name: "routing index", configure: func(c *config.SDKConfig) {
			c.APIKeyUpstreamAuthIndex = map[string]map[string]struct{}{"key": {"account": {}}}
		}},
		{name: "empty allowed set remains restricted", configure: func(c *config.SDKConfig) {
			c.APIKeyUpstreamAuthIndex = map[string]map[string]struct{}{"key": {}}
		}},
		{name: "raw routing group", configure: func(c *config.SDKConfig) {
			c.APIKeyGroups = []config.APIKeyGroup{{ID: "group", APIKeys: []string{"key"}, UpstreamAuthIDs: []string{"account"}}}
		}},
		{name: "ordinary membership group", want: true, configure: func(c *config.SDKConfig) {
			c.APIKeyGroups = []config.APIKeyGroup{{ID: "group", APIKeys: []string{"key"}}}
			c.SanitizeAPIKeyGroups()
		}},
		{name: "empty group", want: true, configure: func(c *config.SDKConfig) {
			c.APIKeyGroups = []config.APIKeyGroup{{ID: "group", UpstreamAuthIDs: []string{"account"}}}
		}},
		{name: "blank routing IDs", want: true, configure: func(c *config.SDKConfig) {
			c.APIKeyGroups = []config.APIKeyGroup{{ID: "group", APIKeys: []string{"key"}, UpstreamAuthIDs: []string{" "}}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.SDKConfig{CodexResponseSteering: true}
			if tt.configure != nil {
				tt.configure(cfg)
			}
			h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(cfg, nil))
			if got := h.responseSteeringEnabled(); got != tt.want {
				t.Fatalf("responseSteeringEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
	for _, h := range []*OpenAIResponsesAPIHandler{nil, {}, {BaseAPIHandler: &handlers.BaseAPIHandler{}}} {
		if h.responseSteeringEnabled() {
			t.Fatal("missing config must not enable steering")
		}
	}
}

type steeringPolicyObservation struct {
	duplex       bool
	model        string
	authID       string
	payloadModel string
}

type steeringPolicyExecutor struct {
	websocketProviderCaptureExecutor
	observations chan steeringPolicyObservation
}

func (e *steeringPolicyExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.observations <- steeringPolicyObservation{
		duplex:       coreexecutor.WebsocketInputFromContext(ctx) != nil,
		model:        req.Model,
		authID:       auth.ID,
		payloadModel: gjson.GetBytes(req.Payload, "model").String(),
	}
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp-policy","model":%q,"output":[]}}`, req.Model))}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func TestResponsesWebsocketResponseSteeringPoliciesUseSerialEntry(t *testing.T) {
	for _, policy := range []string{"rewrite", "quota", "group", "routing-index"} {
		t.Run(policy, func(t *testing.T) {
			authID := "steering-policy-" + policy
			cfg := &config.Config{}
			cfg.Codex.ResponseSteering = true
			cfg.CodexResponseSteering = true
			wantModel := "policy-public"
			switch policy {
			case "rewrite":
				cfg.ModelRewrite = config.ModelRewriteConfig{Enabled: true, Rules: []config.ModelRewriteRule{
					{MatchModels: []string{"policy-public"}, TargetModel: "policy-private"},
				}}
				wantModel = "policy-private"
			case "quota":
				cfg.APIKeyEntries = config.APIKeyEntryList{{APIKey: "client-key", DailyTokenLimit: 1}}
			case "group":
				cfg.APIKeyGroups = []config.APIKeyGroup{{ID: "restricted", APIKeys: []string{"client-key"}, UpstreamAuthIDs: []string{authID}}}
			case "routing-index":
				cfg.APIKeyUpstreamAuthIndex = map[string]map[string]struct{}{"client-key": {authID: {}}}
			}
			executor := &steeringPolicyExecutor{
				websocketProviderCaptureExecutor: websocketProviderCaptureExecutor{provider: "codex"},
				observations:                     make(chan steeringPolicyObservation, 2),
			}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.RegisterExecutor(executor)
			if _, err := manager.Register(context.Background(), &coreauth.Auth{
				ID: authID, Provider: "codex", Status: coreauth.StatusActive,
				Attributes: map[string]string{"websockets": "true"},
			}); err != nil {
				t.Fatal(err)
			}
			registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: "policy-public"}, {ID: "policy-private"}})
			defer registry.GetGlobalRegistry().UnregisterClient(authID)
			h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
			router := gin.New()
			done := make(chan struct{})
			router.GET("/v1/responses", func(c *gin.Context) {
				defer close(done)
				c.Set("userApiKey", "client-key")
				h.ResponsesWebsocket(c)
			})
			server := httptest.NewServer(router)
			defer server.Close()
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			for _, request := range []string{
				`{"type":"response.create","model":"policy-public","input":[]}`,
				`{"type":"response.append","input":[]}`,
			} {
				if err := conn.WriteMessage(websocket.TextMessage, []byte(request)); err != nil {
					t.Fatal(err)
				}
				_, payload, err := conn.ReadMessage()
				if err != nil {
					t.Fatal(err)
				}
				if gjson.GetBytes(payload, "type").String() != "response.completed" {
					t.Fatalf("unexpected response: %s", payload)
				}
				if got := gjson.GetBytes(payload, "response.model").String(); got != "policy-public" {
					t.Fatalf("response model = %q, want masked public model", got)
				}
				select {
				case observed := <-executor.observations:
					if observed.duplex || observed.model != wantModel || observed.authID != authID || observed.payloadModel != wantModel {
						t.Fatalf("serial entry not preserved: %+v; want model=%s auth=%s", observed, wantModel, authID)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("request did not re-enter auth manager execution")
				}
			}
			_ = conn.Close()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("serial websocket handler did not exit")
			}
		})
	}
}
