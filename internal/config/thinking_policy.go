package config

import (
	"strings"

	log "github.com/sirupsen/logrus"
)

// UsageClientIPConfig controls secure client IP resolution for usage statistics.
type UsageClientIPConfig struct {
	TrustedProxies []string `yaml:"trusted-proxies" json:"trusted-proxies"`
}

// ThinkingPolicyConfig configures server-side reasoning effort enforcement.
type ThinkingPolicyConfig struct {
	Codex CodexThinkingPolicyConfig `yaml:"codex" json:"codex"`
}

// CodexThinkingPolicyConfig controls Codex reasoning effort by downstream API key.
type CodexThinkingPolicyConfig struct {
	Enabled       bool     `yaml:"enabled" json:"enabled"`
	DefaultEffort string   `yaml:"default-effort" json:"default-effort"`
	XHighAPIKeys  []string `yaml:"xhigh-api-keys" json:"xhigh-api-keys"`
}

// SanitizeThinkingPolicy normalizes server-side thinking policy settings.
func (cfg *Config) SanitizeThinkingPolicy() {
	if cfg == nil {
		return
	}
	policy := &cfg.ThinkingPolicy.Codex
	policy.DefaultEffort = normalizeCodexThinkingPolicyDefaultEffort(policy.DefaultEffort)
	if len(policy.XHighAPIKeys) == 0 {
		return
	}
	seen := make(map[string]struct{}, len(policy.XHighAPIKeys))
	keys := make([]string, 0, len(policy.XHighAPIKeys))
	for _, apiKey := range policy.XHighAPIKeys {
		apiKey = strings.TrimSpace(apiKey)
		if apiKey == "" {
			continue
		}
		if _, ok := seen[apiKey]; ok {
			continue
		}
		seen[apiKey] = struct{}{}
		keys = append(keys, apiKey)
	}
	policy.XHighAPIKeys = keys
}

func normalizeCodexThinkingPolicyDefaultEffort(effort string) string {
	normalized := strings.ToLower(strings.TrimSpace(effort))
	switch normalized {
	case "":
		return "high"
	case "low", "medium", "high":
		return normalized
	default:
		log.WithField("effort", effort).Warn("unsupported codex thinking policy default effort; falling back to high")
		return "high"
	}
}
