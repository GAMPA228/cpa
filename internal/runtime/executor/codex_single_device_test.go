package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexSingleDeviceTransportParity(t *testing.T) {
	enabled := true
	cfg := &config.Config{AuthDir: t.TempDir(), Codex: config.CodexConfig{SingleDevice: &enabled, IdentityConfuse: true}}
	auth := &cliproxyauth.Auth{ID: "auth-a", Provider: "codex", Metadata: map[string]any{"email": "test@example.com"}}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "codex-tui/0.153.3")
	c.Request.Header.Set("Session-Id", "session-a")
	c.Request.Header.Set("X-Codex-Window-Id", "window-a")
	c.Set("userApiKey", "user-a")
	ctx := context.WithValue(context.Background(), "gin", c)
	body := []byte(`{"model":"gpt-5.5","prompt_cache_key":"cache-a","service_tier":"priority","reasoning":{"effort":"high"},"input":[]}`)
	req := cliproxyexecutor.Request{Model: "gpt-5.5", Payload: body}
	e := &CodexExecutor{cfg: cfg}
	httpReq, httpBody, httpState, err := e.cacheHelper(ctx, sdktranslator.FormatOpenAIResponse, "https://example.com/responses", auth, req, body, body, c.Request.Header)
	if err != nil {
		t.Fatal(err)
	}
	applyCodexHeaders(httpReq, auth, "test-token", true, cfg)
	applyModelHeaderOverrides(httpReq.Header, req.Model, cfg)
	applyCodexRoutingHintHeader(httpReq.Header, auth, httpBody)
	applyCodexIdentityConfuseHeaders(httpReq.Header, &httpState)
	wsBody, wsHeaders, err := applyCodexPromptCacheHeadersWithContext(ctx, sdktranslator.FormatOpenAIResponse, req, body, c.Request.Header)
	if err != nil {
		t.Fatal(err)
	}
	wsBody, wsState, err := helps.PrepareCodexIdentity(ctx, cfg, auth, c.Request.Header, wsBody)
	if err != nil {
		t.Fatal(err)
	}
	wsHeaders = applyCodexWebsocketHeaders(ctx, wsHeaders, auth, "test-token", cfg, c.Request.Header)
	applyModelHeaderOverrides(wsHeaders, req.Model, cfg)
	applyCodexRoutingHintHeader(wsHeaders, auth, wsBody)
	applyCodexIdentityConfuseHeaders(wsHeaders, &codexIdentityConfuseState{singleDevice: wsState})
	for _, name := range []string{"Session-Id", "X-Codex-Window-Id", "X-Codex-Installation-Id", "X-Codex-Routing-Hint"} {
		if httpReq.Header.Get(name) == "" || httpReq.Header.Get(name) != wsHeaders.Get(name) {
			t.Fatalf("transport mismatch for %s", name)
		}
	}
	if headerValueCaseInsensitive(wsHeaders, "session_id") != "" {
		t.Fatal("websocket cache helper reintroduced underscore session")
	}
	if httpReq.Header.Get("Session-Id") == "session-a" {
		t.Fatal("HTTP identity mapping not applied")
	}
	for _, path := range []string{"model", "service_tier", "reasoning.effort"} {
		if gjson.GetBytes(httpBody, path).String() != gjson.GetBytes(body, path).String() {
			t.Fatalf("policy field modified: %s", path)
		}
	}
	if gjson.GetBytes(httpBody, "prompt_cache_key").String() != gjson.GetBytes(wsBody, "prompt_cache_key").String() {
		t.Fatal("cache keys differ across transports")
	}
	if codexIdentityConfuseEnabled(cfg) {
		t.Fatal("legacy mapping also enabled")
	}
	enabled = false
	if codexIdentityConfuseEnabled(cfg) {
		t.Fatal("explicit off still enables legacy mapping")
	}
}
