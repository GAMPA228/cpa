package helps

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func TestApplyCodexThinkingPolicyForcesHighForNonWhitelistedKey(t *testing.T) {
	cfg := &config.Config{ThinkingPolicy: config.ThinkingPolicyConfig{Codex: config.CodexThinkingPolicyConfig{
		Enabled:       true,
		DefaultEffort: "high",
		XHighAPIKeys:  []string{"sk-allowed"},
	}}}
	for _, effort := range []string{"xhigh", "max", "ultra"} {
		t.Run(effort, func(t *testing.T) {
			payload := ApplyCodexThinkingPolicy(cfg, []byte(`{"reasoning":{"effort":"`+effort+`"}}`), cliproxyexecutor.Options{Metadata: map[string]any{
				cliproxyexecutor.UserAPIKeyMetadataKey: "sk-denied",
			}})
			if got := gjson.GetBytes(payload, "reasoning.effort").String(); got != "high" {
				t.Fatalf("effort = %q, want high", got)
			}
		})
	}
}

func TestApplyCodexThinkingPolicyAllowsRequestedHighReasoningForWhitelistedKey(t *testing.T) {
	cfg := &config.Config{ThinkingPolicy: config.ThinkingPolicyConfig{Codex: config.CodexThinkingPolicyConfig{
		Enabled:       true,
		DefaultEffort: "high",
		XHighAPIKeys:  []string{"sk-allowed"},
	}}}
	for _, effort := range []string{"xhigh", "max", "ultra"} {
		t.Run(effort, func(t *testing.T) {
			payload := ApplyCodexThinkingPolicy(cfg, []byte(`{"reasoning":{"effort":"`+effort+`"}}`), cliproxyexecutor.Options{Metadata: map[string]any{
				cliproxyexecutor.UserAPIKeyMetadataKey: "sk-allowed",
			}})
			if got := gjson.GetBytes(payload, "reasoning.effort").String(); got != effort {
				t.Fatalf("effort = %q, want %q", got, effort)
			}
		})
	}
}

func TestApplyCodexThinkingPolicyAllowsRequestedHighReasoningForGroupMember(t *testing.T) {
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{APIKeyGroups: []config.APIKeyGroup{
			{ID: "core-developers", APIKeys: []string{"sk-group-member"}},
		}},
		ThinkingPolicy: config.ThinkingPolicyConfig{Codex: config.CodexThinkingPolicyConfig{
			Enabled:       true,
			DefaultEffort: "high",
			XHighGroups:   []string{"core-developers"},
		}},
	}
	cfg.SanitizeAPIKeyGroups()
	payload := ApplyCodexThinkingPolicy(cfg, []byte(`{"reasoning":{"effort":"ultra"}}`), cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.UserAPIKeyMetadataKey: "sk-group-member",
	}})
	if got := gjson.GetBytes(payload, "reasoning.effort").String(); got != "ultra" {
		t.Fatalf("effort = %q, want ultra", got)
	}
}

func TestApplyCodexThinkingPolicyPreservesSupportedEfforts(t *testing.T) {
	cfg := &config.Config{ThinkingPolicy: config.ThinkingPolicyConfig{Codex: config.CodexThinkingPolicyConfig{
		Enabled:       true,
		DefaultEffort: "high",
		XHighAPIKeys:  []string{"sk-allowed"},
	}}}
	for _, effort := range []string{"low", "medium", "high"} {
		t.Run(effort, func(t *testing.T) {
			payload := ApplyCodexThinkingPolicy(cfg, []byte(`{"reasoning":{"effort":"`+effort+`"}}`), cliproxyexecutor.Options{Metadata: map[string]any{
				cliproxyexecutor.UserAPIKeyMetadataKey: "sk-denied",
			}})
			if got := gjson.GetBytes(payload, "reasoning.effort").String(); got != effort {
				t.Fatalf("effort = %q, want %q", got, effort)
			}
		})
	}
}

func TestApplyCodexThinkingPolicyDefaultsWhitelistedKeyToHigh(t *testing.T) {
	cfg := &config.Config{ThinkingPolicy: config.ThinkingPolicyConfig{Codex: config.CodexThinkingPolicyConfig{
		Enabled:       true,
		DefaultEffort: "high",
		XHighAPIKeys:  []string{"sk-allowed"},
	}}}
	payload := ApplyCodexThinkingPolicy(cfg, []byte(`{"input":"hello"}`), cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.UserAPIKeyMetadataKey: "sk-allowed",
	}})
	if got := gjson.GetBytes(payload, "reasoning.effort").String(); got != "high" {
		t.Fatalf("effort = %q, want high", got)
	}
}
