package helps

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func serviceTierPolicyTestConfig() *config.Config {
	return &config.Config{ServiceTierPolicy: config.ServiceTierPolicyConfig{Codex: config.CodexServiceTierPolicyConfig{
		Enabled:            true,
		AllowedModels:      []string{"gpt-5.6-*"},
		AllowedAPIKeys:     []string{"sk-allowed"},
		AuthorizedMode:     config.CodexServiceTierAuthorizedRequestOnly,
		UnauthorizedAction: config.CodexServiceTierUnauthorizedStrip,
		RejectMessage:      config.DefaultCodexServiceTierRejectMessage,
	}}}
}

func serviceTierPolicyOptions(apiKey, tier string) cliproxyexecutor.Options {
	return cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.UserAPIKeyMetadataKey:  apiKey,
		cliproxyexecutor.ServiceTierMetadataKey: tier,
	}}
}

func TestApplyCodexServiceTierPolicyAllowsFastAlias(t *testing.T) {
	payload, err := ApplyCodexServiceTierPolicy(
		serviceTierPolicyTestConfig(),
		"gpt-5.6-sol",
		[]byte(`{"model":"gpt-5.6-sol"}`),
		serviceTierPolicyOptions("sk-allowed", "fast"),
	)
	if err != nil {
		t.Fatalf("ApplyCodexServiceTierPolicy() error = %v", err)
	}
	if got := gjson.GetBytes(payload, "service_tier").String(); got != "priority" {
		t.Fatalf("service_tier = %q, want priority; payload=%s", got, payload)
	}
}

func TestApplyCodexServiceTierPolicyAllowsGroupMember(t *testing.T) {
	cfg := serviceTierPolicyTestConfig()
	cfg.ServiceTierPolicy.Codex.AllowedAPIKeys = nil
	cfg.ServiceTierPolicy.Codex.AllowedGroups = []string{"fast-users"}
	cfg.APIKeyGroups = []config.APIKeyGroup{{ID: "fast-users", APIKeys: []string{"sk-group-member"}}}
	cfg.SanitizeAPIKeyGroups()

	payload, err := ApplyCodexServiceTierPolicy(
		cfg,
		"gpt-5.6-sol",
		[]byte(`{"model":"gpt-5.6-sol"}`),
		serviceTierPolicyOptions("sk-group-member", "fast"),
	)
	if err != nil {
		t.Fatalf("ApplyCodexServiceTierPolicy() error = %v", err)
	}
	if got := gjson.GetBytes(payload, "service_tier").String(); got != "priority" {
		t.Fatalf("service_tier = %q, want priority; payload=%s", got, payload)
	}
}

func TestApplyCodexServiceTierPolicyStripsUnauthorizedPriority(t *testing.T) {
	payload, err := ApplyCodexServiceTierPolicy(
		serviceTierPolicyTestConfig(),
		"gpt-5.6-sol",
		[]byte(`{"service_tier":"priority"}`),
		serviceTierPolicyOptions("sk-denied", "priority"),
	)
	if err != nil {
		t.Fatalf("ApplyCodexServiceTierPolicy() error = %v", err)
	}
	if gjson.GetBytes(payload, "service_tier").Exists() {
		t.Fatalf("service_tier was not stripped: %s", payload)
	}
}

func TestApplyCodexServiceTierPolicyGatesPayloadOverridePriority(t *testing.T) {
	payload, err := ApplyCodexServiceTierPolicy(
		serviceTierPolicyTestConfig(),
		"gpt-5.6-sol",
		[]byte(`{"service_tier":"priority"}`),
		serviceTierPolicyOptions("sk-denied", "default"),
	)
	if err != nil {
		t.Fatalf("ApplyCodexServiceTierPolicy() error = %v", err)
	}
	if gjson.GetBytes(payload, "service_tier").Exists() {
		t.Fatalf("payload override bypassed policy: %s", payload)
	}
}

func TestApplyCodexServiceTierPolicyRejectsUnauthorizedPriority(t *testing.T) {
	cfg := serviceTierPolicyTestConfig()
	cfg.ServiceTierPolicy.Codex.UnauthorizedAction = config.CodexServiceTierUnauthorizedReject
	cfg.ServiceTierPolicy.Codex.RejectMessage = "请联系管理员"

	_, err := ApplyCodexServiceTierPolicy(
		cfg,
		"gpt-5.6-sol",
		[]byte(`{"service_tier":"priority"}`),
		serviceTierPolicyOptions("sk-denied", "priority"),
	)
	if err == nil {
		t.Fatal("expected policy rejection")
	}
	statusErr, ok := err.(interface{ StatusCode() int })
	if !ok || statusErr.StatusCode() != http.StatusForbidden {
		t.Fatalf("error status = %T %v", err, err)
	}
	requestErr, ok := err.(cliproxyexecutor.RequestScopedError)
	if !ok || !requestErr.IsRequestScoped() {
		t.Fatalf("error is not request scoped: %T", err)
	}
	if got := gjson.Get(err.Error(), "error.message").String(); got != "请联系管理员" {
		t.Fatalf("error message = %q", got)
	}
}

func TestApplyCodexServiceTierPolicyForcesPriorityOnlyForAuthorizedPair(t *testing.T) {
	cfg := serviceTierPolicyTestConfig()
	cfg.ServiceTierPolicy.Codex.AuthorizedMode = config.CodexServiceTierAuthorizedForcePriority

	payload, err := ApplyCodexServiceTierPolicy(
		cfg,
		"gpt-5.6-terra",
		[]byte(`{"service_tier":"default"}`),
		serviceTierPolicyOptions("sk-allowed", "default"),
	)
	if err != nil {
		t.Fatalf("ApplyCodexServiceTierPolicy() error = %v", err)
	}
	if got := gjson.GetBytes(payload, "service_tier").String(); got != "priority" {
		t.Fatalf("service_tier = %q, want priority", got)
	}

	deniedPayload, err := ApplyCodexServiceTierPolicy(
		cfg,
		"gpt-5.5",
		[]byte(`{"service_tier":"default"}`),
		serviceTierPolicyOptions("sk-allowed", "default"),
	)
	if err != nil {
		t.Fatalf("ApplyCodexServiceTierPolicy() denied error = %v", err)
	}
	if got := gjson.GetBytes(deniedPayload, "service_tier").String(); got != "default" {
		t.Fatalf("unrequested non-priority tier changed to %q", got)
	}
}

func TestApplyCodexServiceTierPolicyDisabledPreservesPayload(t *testing.T) {
	cfg := serviceTierPolicyTestConfig()
	cfg.ServiceTierPolicy.Codex.Enabled = false
	input := []byte(`{"service_tier":"fast"}`)
	payload, err := ApplyCodexServiceTierPolicy(cfg, "gpt-5.6-sol", input, serviceTierPolicyOptions("sk-denied", "fast"))
	if err != nil {
		t.Fatalf("ApplyCodexServiceTierPolicy() error = %v", err)
	}
	if string(payload) != string(input) {
		t.Fatalf("payload changed while disabled: %s", payload)
	}
}
