package executor

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
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

	if got := req.Header.Get("User-Agent"); got != codexUserAgent {
		t.Fatalf("User-Agent = %q, want %q", got, codexUserAgent)
	}
	if got := req.Header.Get("Originator"); got != codexOriginator {
		t.Fatalf("Originator = %q, want %q", got, codexOriginator)
	}
	if got := req.Header.Get("Version"); got != codexClientVersion {
		t.Fatalf("Version = %q, want %q", got, codexClientVersion)
	}
	if got := req.Header.Get("OpenAI-Beta"); got != codexResponsesBetaHeader {
		t.Fatalf("OpenAI-Beta = %q, want %q", got, codexResponsesBetaHeader)
	}
}
