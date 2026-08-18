package config

import (
	"slices"
	"strings"
)

// APIKeyGroup defines a reusable set of authenticated downstream API keys.
type APIKeyGroup struct {
	ID              string   `yaml:"id" json:"id"`
	Name            string   `yaml:"name" json:"name"`
	Description     string   `yaml:"description,omitempty" json:"description,omitempty"`
	APIKeys         []string `yaml:"api-keys" json:"api-keys"`
	UpstreamAuthIDs []string `yaml:"upstream-auth-ids,omitempty" json:"upstream-auth-ids,omitempty"`
}

// SanitizeAPIKeyGroups normalizes group definitions and builds the runtime membership index.
func (cfg *SDKConfig) SanitizeAPIKeyGroups() {
	if cfg == nil {
		return
	}

	groups := make([]APIKeyGroup, 0, len(cfg.APIKeyGroups))
	index := make(map[string]map[string]struct{}, len(cfg.APIKeyGroups))
	authRoutingIndex := make(map[string]map[string]struct{})
	for _, group := range cfg.APIKeyGroups {
		group.ID = normalizeAPIKeyGroupID(group.ID)
		if group.ID == "" {
			continue
		}
		if _, exists := index[group.ID]; exists {
			continue
		}
		group.Name = strings.TrimSpace(group.Name)
		if group.Name == "" {
			group.Name = group.ID
		}
		group.Description = strings.TrimSpace(group.Description)
		group.APIKeys = sanitizeModelRewriteStringList(group.APIKeys, false)
		group.UpstreamAuthIDs = sanitizeModelRewriteStringList(group.UpstreamAuthIDs, false)

		members := make(map[string]struct{}, len(group.APIKeys))
		for _, apiKey := range group.APIKeys {
			members[apiKey] = struct{}{}
		}
		index[group.ID] = members
		if len(group.UpstreamAuthIDs) > 0 {
			for _, apiKey := range group.APIKeys {
				allowed := authRoutingIndex[apiKey]
				if allowed == nil {
					allowed = make(map[string]struct{}, len(group.UpstreamAuthIDs))
					authRoutingIndex[apiKey] = allowed
				}
				for _, authID := range group.UpstreamAuthIDs {
					allowed[authID] = struct{}{}
				}
			}
		}
		groups = append(groups, group)
	}

	if len(groups) == 0 {
		cfg.APIKeyGroups = nil
		cfg.APIKeyGroupIndex = nil
		cfg.APIKeyUpstreamAuthIndex = nil
		return
	}
	cfg.APIKeyGroups = groups
	cfg.APIKeyGroupIndex = index
	if len(authRoutingIndex) == 0 {
		cfg.APIKeyUpstreamAuthIndex = nil
	} else {
		cfg.APIKeyUpstreamAuthIndex = authRoutingIndex
	}
}

// ResolveUpstreamAuthIDs returns the Codex auth IDs assigned to a downstream API key.
// A false restricted result preserves legacy unrestricted routing.
func (cfg *SDKConfig) ResolveUpstreamAuthIDs(userAPIKey string) (authIDs []string, restricted bool) {
	if cfg == nil {
		return nil, false
	}
	userAPIKey = strings.TrimSpace(userAPIKey)
	if userAPIKey == "" {
		return nil, false
	}
	allowed, ok := cfg.APIKeyUpstreamAuthIndex[userAPIKey]
	if cfg.APIKeyUpstreamAuthIndex == nil {
		allowed = make(map[string]struct{})
		for _, group := range cfg.APIKeyGroups {
			member := false
			for _, apiKey := range group.APIKeys {
				if strings.TrimSpace(apiKey) == userAPIKey {
					member = true
					break
				}
			}
			if !member {
				continue
			}
			for _, authID := range group.UpstreamAuthIDs {
				authID = strings.TrimSpace(authID)
				if authID != "" {
					allowed[authID] = struct{}{}
				}
			}
		}
		ok = len(allowed) > 0
	}
	if !ok {
		return nil, false
	}
	authIDs = make([]string, 0, len(allowed))
	for authID := range allowed {
		authIDs = append(authIDs, authID)
	}
	slices.Sort(authIDs)
	return authIDs, true
}

// APIKeyMatchesPolicy reports whether a downstream API key matches a direct entry or group reference.
func (cfg *SDKConfig) APIKeyMatchesPolicy(userAPIKey string, directAPIKeys, groupIDs []string) bool {
	userAPIKey = strings.TrimSpace(userAPIKey)
	if userAPIKey == "" {
		return false
	}
	for _, apiKey := range directAPIKeys {
		if strings.TrimSpace(apiKey) == userAPIKey {
			return true
		}
	}
	if cfg == nil || len(groupIDs) == 0 {
		return false
	}

	for _, groupID := range groupIDs {
		groupID = normalizeAPIKeyGroupID(groupID)
		if groupID == "" {
			continue
		}
		if members, ok := cfg.APIKeyGroupIndex[groupID]; ok {
			if _, allowed := members[userAPIKey]; allowed {
				return true
			}
			continue
		}
		// Support manually constructed SDKConfig values that have not passed through the loader.
		for _, group := range cfg.APIKeyGroups {
			if normalizeAPIKeyGroupID(group.ID) != groupID {
				continue
			}
			for _, apiKey := range group.APIKeys {
				if strings.TrimSpace(apiKey) == userAPIKey {
					return true
				}
			}
			break
		}
	}
	return false
}

func sanitizeAPIKeyGroupReferences(groupIDs []string) []string {
	if len(groupIDs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(groupIDs))
	out := make([]string, 0, len(groupIDs))
	for _, groupID := range groupIDs {
		groupID = normalizeAPIKeyGroupID(groupID)
		if groupID == "" {
			continue
		}
		if _, exists := seen[groupID]; exists {
			continue
		}
		seen[groupID] = struct{}{}
		out = append(out, groupID)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeAPIKeyGroupID(groupID string) string {
	return strings.ToLower(strings.TrimSpace(groupID))
}
