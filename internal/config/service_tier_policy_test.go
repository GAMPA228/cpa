package config

import "testing"

func TestSanitizeServiceTierPolicyDefaultsAndDeduplicates(t *testing.T) {
	cfg := &Config{ServiceTierPolicy: ServiceTierPolicyConfig{Codex: CodexServiceTierPolicyConfig{
		AllowedModels:      []string{" gpt-5.6-* ", "GPT-5.6-*", ""},
		AllowedAPIKeys:     []string{" sk-one ", "sk-one", ""},
		AuthorizedMode:     "unsupported",
		UnauthorizedAction: "unsupported",
	}}}

	cfg.SanitizeServiceTierPolicy()
	policy := cfg.ServiceTierPolicy.Codex
	if len(policy.AllowedModels) != 1 || policy.AllowedModels[0] != "gpt-5.6-*" {
		t.Fatalf("allowed models = %#v", policy.AllowedModels)
	}
	if len(policy.AllowedAPIKeys) != 1 || policy.AllowedAPIKeys[0] != "sk-one" {
		t.Fatalf("allowed API keys = %#v", policy.AllowedAPIKeys)
	}
	if policy.AuthorizedMode != CodexServiceTierAuthorizedRequestOnly {
		t.Fatalf("authorized mode = %q", policy.AuthorizedMode)
	}
	if policy.UnauthorizedAction != CodexServiceTierUnauthorizedStrip {
		t.Fatalf("unauthorized action = %q", policy.UnauthorizedAction)
	}
	if policy.RejectMessage != DefaultCodexServiceTierRejectMessage {
		t.Fatalf("reject message = %q", policy.RejectMessage)
	}
}

func TestCodexServiceTierPolicyAllowsModelAndAPIKey(t *testing.T) {
	policy := CodexServiceTierPolicyConfig{
		AllowedModels:  []string{"gpt-5.6-*"},
		AllowedAPIKeys: []string{"sk-allowed"},
	}

	if !policy.Allows("team/gpt-5.6-sol(ultra)", "sk-allowed") {
		t.Fatal("expected model and API key to be allowed")
	}
	if policy.Allows("gpt-5.5", "sk-allowed") {
		t.Fatal("unexpected model match")
	}
	if policy.Allows("gpt-5.6-sol", "sk-denied") {
		t.Fatal("unexpected API key match")
	}
}
