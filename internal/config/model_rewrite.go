package config

import "strings"

// SanitizeModelRewrite normalizes model rewrite rules.
func (cfg *SDKConfig) SanitizeModelRewrite() {
	if cfg == nil || len(cfg.ModelRewrite.Rules) == 0 {
		return
	}
	out := make([]ModelRewriteRule, 0, len(cfg.ModelRewrite.Rules))
	for _, rule := range cfg.ModelRewrite.Rules {
		rule.TargetModel = strings.TrimSpace(rule.TargetModel)
		if rule.TargetModel == "" {
			continue
		}
		rule.MatchModels = sanitizeModelRewriteStringList(rule.MatchModels, true)
		if len(rule.MatchModels) == 0 {
			continue
		}
		rule.BypassAPIKeys = sanitizeModelRewriteStringList(rule.BypassAPIKeys, false)
		out = append(out, rule)
	}
	cfg.ModelRewrite.Rules = out
}

func sanitizeModelRewriteStringList(values []string, caseInsensitive bool) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := value
		if caseInsensitive {
			key = strings.ToLower(value)
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// RewriteModelForAPIKey returns the effective model for a downstream API key.
// It preserves a client thinking suffix when the target model does not specify one.
func (cfg *SDKConfig) RewriteModelForAPIKey(userAPIKey, model string) (string, bool) {
	if cfg == nil || !cfg.ModelRewrite.Enabled {
		return model, false
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return model, false
	}
	for _, rule := range cfg.ModelRewrite.Rules {
		if !modelRewriteRuleMatches(rule, model) {
			continue
		}
		if modelRewriteBypassAPIKey(rule.BypassAPIKeys, userAPIKey) {
			return model, false
		}
		target := modelRewriteApplySuffix(model, rule.TargetModel)
		if target == "" || strings.EqualFold(target, model) {
			return model, false
		}
		return target, true
	}
	return model, false
}

func modelRewriteRuleMatches(rule ModelRewriteRule, model string) bool {
	candidates := modelRewriteCandidates(model)
	for _, pattern := range rule.MatchModels {
		for _, candidate := range candidates {
			if modelRewriteWildcardMatch(pattern, candidate) {
				return true
			}
		}
	}
	return false
}

func modelRewriteBypassAPIKey(keys []string, userAPIKey string) bool {
	userAPIKey = strings.TrimSpace(userAPIKey)
	if userAPIKey == "" {
		return false
	}
	for _, key := range keys {
		if strings.TrimSpace(key) == userAPIKey {
			return true
		}
	}
	return false
}

func modelRewriteCandidates(model string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, 4)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}

	add(model)
	base, _ := modelRewriteSplitSuffix(model)
	add(base)
	if idx := strings.LastIndex(base, "/"); idx >= 0 && idx < len(base)-1 {
		add(base[idx+1:])
	}
	if idx := strings.LastIndex(model, "/"); idx >= 0 && idx < len(model)-1 {
		add(model[idx+1:])
	}
	return out
}

func modelRewriteApplySuffix(sourceModel, targetModel string) string {
	targetModel = strings.TrimSpace(targetModel)
	if targetModel == "" {
		return ""
	}
	_, sourceSuffix := modelRewriteSplitSuffix(sourceModel)
	_, targetSuffix := modelRewriteSplitSuffix(targetModel)
	if sourceSuffix == "" || targetSuffix != "" {
		return targetModel
	}
	return targetModel + "(" + sourceSuffix + ")"
}

func modelRewriteSplitSuffix(model string) (string, string) {
	model = strings.TrimSpace(model)
	if !strings.HasSuffix(model, ")") {
		return model, ""
	}
	idx := strings.LastIndex(model, "(")
	if idx <= 0 {
		return model, ""
	}
	base := strings.TrimSpace(model[:idx])
	suffix := strings.TrimSuffix(model[idx+1:], ")")
	suffix = strings.TrimSpace(suffix)
	if base == "" || suffix == "" {
		return model, ""
	}
	return base, suffix
}

func modelRewriteWildcardMatch(pattern, value string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	value = strings.ToLower(strings.TrimSpace(value))
	if pattern == "" || value == "" {
		return false
	}
	if pattern == "*" {
		return true
	}

	pRunes := []rune(pattern)
	vRunes := []rune(value)
	pIdx, vIdx := 0, 0
	starIdx, matchIdx := -1, 0
	for vIdx < len(vRunes) {
		if pIdx < len(pRunes) && (pRunes[pIdx] == '?' || pRunes[pIdx] == vRunes[vIdx]) {
			pIdx++
			vIdx++
			continue
		}
		if pIdx < len(pRunes) && pRunes[pIdx] == '*' {
			starIdx = pIdx
			matchIdx = vIdx
			pIdx++
			continue
		}
		if starIdx != -1 {
			pIdx = starIdx + 1
			matchIdx++
			vIdx = matchIdx
			continue
		}
		return false
	}
	for pIdx < len(pRunes) && pRunes[pIdx] == '*' {
		pIdx++
	}
	return pIdx == len(pRunes)
}
