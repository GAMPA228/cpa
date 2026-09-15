package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexFastRequestKeepsCanonicalIdentityAfterModelOverride(t *testing.T) {
	cfg := &config.Config{ServiceTierPolicy: config.ServiceTierPolicyConfig{Codex: config.CodexServiceTierPolicyConfig{
		Enabled:        true,
		AllowedModels:  []string{"gpt-5.6-*"},
		AllowedAPIKeys: []string{"sk-allowed"},
	}}}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.UserAPIKeyMetadataKey:  "sk-allowed",
		cliproxyexecutor.ServiceTierMetadataKey: "fast",
	}}

	body, err := helps.ApplyCodexServiceTierPolicy(cfg, "gpt-5.6-luna", []byte(`{"model":"gpt-5.6-luna"}`), opts)
	if err != nil {
		t.Fatalf("ApplyCodexServiceTierPolicy() error = %v", err)
	}
	if got := gjson.GetBytes(body, "service_tier").String(); got != "priority" {
		t.Fatalf("service_tier = %q, want priority; body=%s", got, body)
	}

	req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	applyCodexHeaders(req, nil, "oauth-token", true, cfg)
	applyModelHeaderOverrides(req.Header, "gpt-5.6-luna", cfg)

	const wantUserAgent = "codex-tui/0.154.0 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.154.0)"
	if got := req.Header.Get("User-Agent"); got != wantUserAgent {
		t.Fatalf("User-Agent = %q, want %q", got, wantUserAgent)
	}
	if got := req.Header.Get("Originator"); got != "codex-tui" {
		t.Fatalf("Originator = %q, want %q", got, "codex-tui")
	}
	if got := req.Header.Get("Version"); got != codexClientVersion {
		t.Fatalf("Version = %q, want %q", got, codexClientVersion)
	}
	if got := req.Header.Get("OpenAI-Beta"); got != codexResponsesBetaHeader {
		t.Fatalf("OpenAI-Beta = %q, want %q", got, codexResponsesBetaHeader)
	}
}

func TestApplyCodexRoutingHintHeaderForOAuthFastRequest(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"access_token": "oauth-token"},
	}
	headers := http.Header{}

	applyCodexRoutingHintHeader(headers, auth, []byte(`{"model":"gpt-5.5","service_tier":"priority"}`))

	if got := headers.Get(codexRoutingHintHeader); got != "model=gpt-5.5;tier=priority" {
		t.Fatalf("%s = %q, want %q", codexRoutingHintHeader, got, "model=gpt-5.5;tier=priority")
	}
}

func TestApplyCodexRoutingHintHeaderForOAuthStandardRequest(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"access_token": "oauth-token"},
	}
	headers := http.Header{}

	applyCodexRoutingHintHeader(headers, auth, []byte(`{"model":"gpt-5.6-terra"}`))

	if got := headers.Get(codexRoutingHintHeader); got != "model=gpt-5.6-terra" {
		t.Fatalf("%s = %q, want %q", codexRoutingHintHeader, got, "model=gpt-5.6-terra")
	}
}

func TestApplyCodexRoutingHintHeaderOmitsAPIKeyRoutes(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider:   "codex",
		Attributes: map[string]string{"api_key": "sk-test"},
	}
	headers := http.Header{}

	applyCodexRoutingHintHeader(headers, auth, []byte(`{"model":"gpt-5.5","service_tier":"priority"}`))

	if got := headers.Get(codexRoutingHintHeader); got != "" {
		t.Fatalf("%s = %q, want empty", codexRoutingHintHeader, got)
	}
}

func TestApplyCodexRoutingHintHeaderRejectsInvalidValues(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"access_token": "oauth-token"},
	}
	headers := http.Header{}

	applyCodexRoutingHintHeader(headers, auth, []byte(`{"model":"gpt-5.5;route=other","service_tier":"priority"}`))

	if got := headers.Get(codexRoutingHintHeader); got != "" {
		t.Fatalf("%s = %q, want empty", codexRoutingHintHeader, got)
	}
}

func TestCodexExecutorForwardsFastRoutingHintToOAuthUpstream(t *testing.T) {
	var gotRoutingHint string
	var gotUserAgent string
	var gotOriginator string
	var gotVersion string
	var gotBeta string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRoutingHint = r.Header.Get(codexRoutingHintHeader)
		gotUserAgent = r.Header.Get("User-Agent")
		gotOriginator = r.Header.Get("Originator")
		gotVersion = r.Header.Get("Version")
		gotBeta = r.Header.Get("OpenAI-Beta")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"output\":[],\"service_tier\":\"priority\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer server.Close()

	cfg := &config.Config{
		SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
		ServiceTierPolicy: config.ServiceTierPolicyConfig{Codex: config.CodexServiceTierPolicyConfig{
			Enabled:        true,
			AllowedModels:  []string{"gpt-5.5"},
			AllowedAPIKeys: []string{"sk-allowed"},
		}},
	}
	executor := NewCodexExecutor(cfg)
	auth := &cliproxyauth.Auth{
		Provider:   "codex",
		Attributes: map[string]string{"base_url": server.URL, "plan_type": "free"},
		Metadata:   map[string]any{"access_token": "oauth-token"},
	}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Metadata: map[string]any{
			cliproxyexecutor.UserAPIKeyMetadataKey:  "sk-allowed",
			cliproxyexecutor.ServiceTierMetadataKey: "fast",
		},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotRoutingHint != "model=gpt-5.5;tier=priority" {
		t.Fatalf("%s = %q, want %q", codexRoutingHintHeader, gotRoutingHint, "model=gpt-5.5;tier=priority")
	}
	if gotUserAgent != "codex-tui/0.154.0 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.154.0)" {
		t.Fatalf("User-Agent = %q, want canonical Codex TUI identity", gotUserAgent)
	}
	if gotOriginator != "codex-tui" {
		t.Fatalf("Originator = %q, want codex-tui", gotOriginator)
	}
	if gotVersion != "0.154.0" {
		t.Fatalf("Version = %q, want 0.154.0", gotVersion)
	}
	if gotBeta != "responses=experimental" {
		t.Fatalf("OpenAI-Beta = %q, want responses=experimental", gotBeta)
	}
}
