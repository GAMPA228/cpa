package config

import "strings"

const (
	// CodexServiceTierAuthorizedRequestOnly allows priority only when the client requests it.
	CodexServiceTierAuthorizedRequestOnly = "request-only"
	// CodexServiceTierAuthorizedForcePriority forces priority for authorized requests.
	CodexServiceTierAuthorizedForcePriority = "force-priority"
	// CodexServiceTierUnauthorizedStrip silently removes unauthorized priority requests.
	CodexServiceTierUnauthorizedStrip = "strip"
	// CodexServiceTierUnauthorizedReject rejects unauthorized priority requests.
	CodexServiceTierUnauthorizedReject = "reject"
	// DefaultCodexServiceTierRejectMessage is returned when unauthorized priority is rejected.
	DefaultCodexServiceTierRejectMessage = "Fast 模式未授权，请联系管理员"
)

// ServiceTierPolicyConfig configures provider-specific service tier policies.
type ServiceTierPolicyConfig struct {
	Codex CodexServiceTierPolicyConfig `yaml:"codex" json:"codex"`
}

// CodexServiceTierPolicyConfig controls Codex priority service tier by model and downstream API key.
type CodexServiceTierPolicyConfig struct {
	Enabled            bool     `yaml:"enabled" json:"enabled"`
	AllowedModels      []string `yaml:"allowed-models" json:"allowed-models"`
	AllowedAPIKeys     []string `yaml:"allowed-api-keys" json:"allowed-api-keys"`
	AllowedGroups      []string `yaml:"allowed-groups,omitempty" json:"allowed-groups,omitempty"`
	AuthorizedMode     string   `yaml:"authorized-mode" json:"authorized-mode"`
	UnauthorizedAction string   `yaml:"unauthorized-action" json:"unauthorized-action"`
	RejectMessage      string   `yaml:"reject-message" json:"reject-message"`
}

// SanitizeServiceTierPolicy normalizes Codex service tier policy settings.
func (cfg *Config) SanitizeServiceTierPolicy() {
	if cfg == nil {
		return
	}

	policy := &cfg.ServiceTierPolicy.Codex
	policy.AllowedModels = sanitizeModelRewriteStringList(policy.AllowedModels, true)
	policy.AllowedAPIKeys = sanitizeModelRewriteStringList(policy.AllowedAPIKeys, false)
	policy.AllowedGroups = sanitizeAPIKeyGroupReferences(policy.AllowedGroups)

	switch strings.ToLower(strings.TrimSpace(policy.AuthorizedMode)) {
	case CodexServiceTierAuthorizedForcePriority:
		policy.AuthorizedMode = CodexServiceTierAuthorizedForcePriority
	default:
		policy.AuthorizedMode = CodexServiceTierAuthorizedRequestOnly
	}

	switch strings.ToLower(strings.TrimSpace(policy.UnauthorizedAction)) {
	case CodexServiceTierUnauthorizedReject:
		policy.UnauthorizedAction = CodexServiceTierUnauthorizedReject
	default:
		policy.UnauthorizedAction = CodexServiceTierUnauthorizedStrip
	}

	policy.RejectMessage = strings.TrimSpace(policy.RejectMessage)
	if policy.RejectMessage == "" {
		policy.RejectMessage = DefaultCodexServiceTierRejectMessage
	}
}

// CodexServiceTierAllows reports whether the model and downstream API key or group are authorized.
func (cfg *Config) CodexServiceTierAllows(model, userAPIKey string) bool {
	if cfg == nil {
		return false
	}
	policy := cfg.ServiceTierPolicy.Codex
	if !policy.allowsModel(model) {
		return false
	}
	return cfg.APIKeyMatchesPolicy(userAPIKey, policy.AllowedAPIKeys, policy.AllowedGroups)
}

// Allows reports whether the effective model and downstream API key both match the whitelist.
func (policy CodexServiceTierPolicyConfig) Allows(model, userAPIKey string) bool {
	userAPIKey = strings.TrimSpace(userAPIKey)
	if userAPIKey == "" || !policy.allowsModel(model) {
		return false
	}

	keyAllowed := false
	for _, allowed := range policy.AllowedAPIKeys {
		if strings.TrimSpace(allowed) == userAPIKey {
			keyAllowed = true
			break
		}
	}
	return keyAllowed
}

func (policy CodexServiceTierPolicyConfig) allowsModel(model string) bool {
	for _, pattern := range policy.AllowedModels {
		for _, candidate := range modelRewriteCandidates(model) {
			if modelRewriteWildcardMatch(pattern, candidate) {
				return true
			}
		}
	}
	return false
}
