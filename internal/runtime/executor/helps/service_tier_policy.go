package helps

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const codexPriorityServiceTier = "priority"

// CodexServiceTierPolicyError is a request-scoped rejection produced by the Codex priority policy.
type CodexServiceTierPolicyError struct {
	message string
}

// Error returns an OpenAI-compatible error response body.
func (e *CodexServiceTierPolicyError) Error() string {
	message := config.DefaultCodexServiceTierRejectMessage
	if e != nil && strings.TrimSpace(e.message) != "" {
		message = strings.TrimSpace(e.message)
	}
	payload := struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}{}
	payload.Error.Message = message
	payload.Error.Type = "permission_error"
	payload.Error.Code = "service_tier_not_allowed"
	encoded, err := json.Marshal(payload)
	if err != nil {
		return message
	}
	return string(encoded)
}

// StatusCode returns the downstream HTTP status for a rejected priority request.
func (*CodexServiceTierPolicyError) StatusCode() int { return http.StatusForbidden }

// IsRequestScoped prevents credential fallback and cooldown for policy rejections.
func (*CodexServiceTierPolicyError) IsRequestScoped() bool { return true }

// ApplyCodexServiceTierPolicy enforces the server-owned priority service tier policy.
// It evaluates the effective upstream model and authenticated downstream API key.
func ApplyCodexServiceTierPolicy(cfg *config.Config, model string, payload []byte, opts cliproxyexecutor.Options) ([]byte, error) {
	if cfg == nil || len(payload) == 0 || !cfg.ServiceTierPolicy.Codex.Enabled {
		return payload, nil
	}

	policy := cfg.ServiceTierPolicy.Codex
	requestedTier := codexRequestedServiceTier(payload, opts)
	priorityRequested := requestedTier == "fast" || requestedTier == codexPriorityServiceTier
	authorized := policy.Allows(model, UserAPIKeyFromOptions(opts))

	if authorized {
		if policy.AuthorizedMode == config.CodexServiceTierAuthorizedForcePriority || priorityRequested {
			updated, err := sjson.SetBytes(payload, "service_tier", codexPriorityServiceTier)
			if err == nil {
				return updated, nil
			}
		}
		return payload, nil
	}

	if !priorityRequested {
		return payload, nil
	}
	if policy.UnauthorizedAction == config.CodexServiceTierUnauthorizedReject {
		return payload, &CodexServiceTierPolicyError{message: policy.RejectMessage}
	}
	updated, err := sjson.DeleteBytes(payload, "service_tier")
	if err != nil {
		return payload, nil
	}
	return updated, nil
}

func codexRequestedServiceTier(payload []byte, opts cliproxyexecutor.Options) string {
	metadataTier := ""
	if opts.Metadata != nil {
		if value, ok := opts.Metadata[cliproxyexecutor.ServiceTierMetadataKey]; ok {
			switch tier := value.(type) {
			case string:
				metadataTier = strings.ToLower(strings.TrimSpace(tier))
			case []byte:
				metadataTier = strings.ToLower(strings.TrimSpace(string(tier)))
			}
		}
	}
	payloadTier := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "service_tier").String()))
	if metadataTier == "fast" || metadataTier == codexPriorityServiceTier {
		return metadataTier
	}
	if payloadTier == "fast" || payloadTier == codexPriorityServiceTier {
		return payloadTier
	}
	if metadataTier != "" && metadataTier != "auto" {
		return metadataTier
	}
	return payloadTier
}
