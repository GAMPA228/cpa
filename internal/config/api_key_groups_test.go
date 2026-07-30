package config

import "testing"

func TestSanitizeAPIKeyGroupsBuildsMembershipIndex(t *testing.T) {
	cfg := &SDKConfig{APIKeyGroups: []APIKeyGroup{
		{ID: " Core-Devs ", Name: " Core developers ", APIKeys: []string{" sk-a ", "sk-a", "sk-b"}},
		{ID: "core-devs", Name: "duplicate", APIKeys: []string{"sk-ignored"}},
		{ID: " ", APIKeys: []string{"sk-empty"}},
	}}

	cfg.SanitizeAPIKeyGroups()

	if len(cfg.APIKeyGroups) != 1 {
		t.Fatalf("groups = %#v, want one normalized group", cfg.APIKeyGroups)
	}
	group := cfg.APIKeyGroups[0]
	if group.ID != "core-devs" || group.Name != "Core developers" {
		t.Fatalf("group = %#v", group)
	}
	if len(group.APIKeys) != 2 || group.APIKeys[0] != "sk-a" || group.APIKeys[1] != "sk-b" {
		t.Fatalf("group API keys = %#v", group.APIKeys)
	}
	if !cfg.APIKeyMatchesPolicy("sk-b", nil, []string{"CORE-DEVS"}) {
		t.Fatal("expected indexed group member to match")
	}
	if cfg.APIKeyMatchesPolicy("sk-ignored", nil, []string{"core-devs"}) {
		t.Fatal("duplicate group unexpectedly contributed members")
	}
}

func TestAPIKeyMatchesPolicyCombinesDirectAndGroupEntries(t *testing.T) {
	cfg := &SDKConfig{APIKeyGroups: []APIKeyGroup{
		{ID: "developers", APIKeys: []string{"sk-group"}},
	}}

	if !cfg.APIKeyMatchesPolicy("sk-direct", []string{"sk-direct"}, []string{"missing"}) {
		t.Fatal("expected direct API key to match without a valid group")
	}
	if !cfg.APIKeyMatchesPolicy("sk-group", []string{"sk-direct"}, []string{"developers"}) {
		t.Fatal("expected unsanitized group fallback to match")
	}
	if cfg.APIKeyMatchesPolicy("sk-denied", []string{"sk-direct"}, []string{"developers"}) {
		t.Fatal("unexpected API key match")
	}
}

func TestParseConfigBytesKeepsLegacyPolicyWithoutGroups(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
api-keys:
  - sk-direct
thinking-policy:
  codex:
    enabled: true
    xhigh-api-keys:
      - sk-direct
service-tier-policy:
  codex:
    enabled: true
    allowed-models: ["gpt-5.6-*"]
    allowed-api-keys: ["sk-direct"]
model-rewrite:
  enabled: true
  rules:
    - match-models: ["gpt-5.6-*"]
      target-model: "gpt-5.5"
      bypass-api-keys: ["sk-direct"]
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if len(cfg.APIKeyGroups) != 0 {
		t.Fatalf("APIKeyGroups = %#v, want empty", cfg.APIKeyGroups)
	}
	if !cfg.CodexServiceTierAllows("gpt-5.6-sol", "sk-direct") {
		t.Fatal("legacy direct Fast whitelist stopped matching")
	}
	if _, rewritten := cfg.RewriteModelForAPIKey("sk-direct", "gpt-5.6-sol"); rewritten {
		t.Fatal("legacy direct model rewrite bypass stopped matching")
	}
}
