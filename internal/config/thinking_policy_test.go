package config

import "testing"

func TestParseConfigBytesThinkingPolicySanitizesCodex(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
thinking-policy:
  codex:
    enabled: true
    default-effort: medium
    xhigh-api-keys:
      - " sk-allowed "
      - ""
      - "sk-allowed"
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	policy := cfg.ThinkingPolicy.Codex
	if !policy.Enabled {
		t.Fatalf("policy enabled = false, want true")
	}
	if policy.DefaultEffort != "high" {
		t.Fatalf("default effort = %q, want high", policy.DefaultEffort)
	}
	if len(policy.XHighAPIKeys) != 1 || policy.XHighAPIKeys[0] != "sk-allowed" {
		t.Fatalf("xhigh keys = %#v, want [sk-allowed]", policy.XHighAPIKeys)
	}
}
