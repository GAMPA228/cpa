package helps

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	codexThinkingEffortLow    = "low"
	codexThinkingEffortMedium = "medium"
	codexThinkingEffortHigh   = "high"
	codexThinkingEffortXHigh  = "xhigh"
)

// ApplyCodexThinkingPolicy enforces server-owned Codex reasoning effort policy.
// Missing reasoning effort defaults to high. Explicit low/medium/high values are
// preserved. Whitelisted downstream API keys may keep xhigh when requested;
// non-whitelisted xhigh requests are downgraded to high.
func ApplyCodexThinkingPolicy(cfg *config.Config, payload []byte, opts cliproxyexecutor.Options) []byte {
	if cfg == nil || len(payload) == 0 || !cfg.ThinkingPolicy.Codex.Enabled {
		return payload
	}

	effort := codexPayloadReasoningEffort(payload)
	switch effort {
	case codexThinkingEffortLow, codexThinkingEffortMedium, codexThinkingEffortHigh:
	case codexThinkingEffortXHigh:
		if !codexPolicyAllowsXHigh(cfg.ThinkingPolicy.Codex, UserAPIKeyFromOptions(opts)) {
			effort = codexThinkingEffortHigh
		}
	default:
		effort = cfg.ThinkingPolicy.Codex.DefaultEffort
		if effort == "" {
			effort = codexThinkingEffortHigh
		}
	}

	out, err := sjson.SetBytes(payload, "reasoning.effort", effort)
	if err != nil {
		return payload
	}
	return out
}

// UserAPIKeyFromOptions returns the authenticated downstream API key principal carried by handlers.
func UserAPIKeyFromOptions(opts cliproxyexecutor.Options) string {
	if opts.Metadata == nil {
		return ""
	}
	value, ok := opts.Metadata[cliproxyexecutor.UserAPIKeyMetadataKey]
	if !ok {
		return ""
	}
	apiKey, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(apiKey)
}

func codexPolicyAllowsXHigh(policy config.CodexThinkingPolicyConfig, userAPIKey string) bool {
	userAPIKey = strings.TrimSpace(userAPIKey)
	if userAPIKey == "" {
		return false
	}
	for _, allowed := range policy.XHighAPIKeys {
		if strings.TrimSpace(allowed) == userAPIKey {
			return true
		}
	}
	return false
}

func codexPayloadReasoningEffort(payload []byte) string {
	return strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "reasoning.effort").String()))
}
