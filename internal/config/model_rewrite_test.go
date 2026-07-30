package config

import "testing"

func TestSanitizeModelRewriteDropsInvalidRules(t *testing.T) {
	cfg := &SDKConfig{
		ModelRewrite: ModelRewriteConfig{
			Enabled: true,
			Rules: []ModelRewriteRule{
				{MatchModels: []string{"gpt-5.5"}, TargetModel: ""},
				{MatchModels: []string{"  ", ""}, TargetModel: "gpt-5.4"},
				{
					MatchModels:          []string{" GPT-5.5 ", "gpt-5.5"},
					TargetModel:          " gpt-5.4 ",
					TargetThinkingEffort: " Ultra ",
					BypassAPIKeys:        []string{" sk-a ", "sk-a", "SK-A"},
					BypassGroups:         []string{" Core-Devs ", "core-devs"},
				},
			},
		},
	}

	cfg.SanitizeModelRewrite()

	if got := len(cfg.ModelRewrite.Rules); got != 1 {
		t.Fatalf("rules = %d, want 1", got)
	}
	rule := cfg.ModelRewrite.Rules[0]
	if got := rule.TargetModel; got != "gpt-5.4" {
		t.Fatalf("target model = %q, want %q", got, "gpt-5.4")
	}
	if got := rule.TargetThinkingEffort; got != "ultra" {
		t.Fatalf("target thinking effort = %q, want %q", got, "ultra")
	}
	if got := rule.MatchModels; len(got) != 1 || got[0] != "GPT-5.5" {
		t.Fatalf("match models = %#v, want trimmed case-insensitive dedupe", got)
	}
	if got := rule.BypassAPIKeys; len(got) != 2 || got[0] != "sk-a" || got[1] != "SK-A" {
		t.Fatalf("bypass api keys = %#v, want case-sensitive dedupe", got)
	}
	if got := rule.BypassGroups; len(got) != 1 || got[0] != "core-devs" {
		t.Fatalf("bypass groups = %#v, want normalized dedupe", got)
	}
}

func TestRewriteModelForAPIKeyExactMatch(t *testing.T) {
	cfg := &SDKConfig{
		ModelRewrite: ModelRewriteConfig{
			Enabled: true,
			Rules: []ModelRewriteRule{
				{MatchModels: []string{"gpt-5.5"}, TargetModel: "gpt-5.4"},
			},
		},
	}

	got, rewritten := cfg.RewriteModelForAPIKey("sk-user", "gpt-5.5")
	if !rewritten || got != "gpt-5.4" {
		t.Fatalf("RewriteModelForAPIKey() = %q/%v, want gpt-5.4/true", got, rewritten)
	}
}

func TestRewriteModelForAPIKeyWildcardMatch(t *testing.T) {
	cfg := &SDKConfig{
		ModelRewrite: ModelRewriteConfig{
			Enabled: true,
			Rules: []ModelRewriteRule{
				{MatchModels: []string{"gpt-5.*"}, TargetModel: "gpt-5.4"},
			},
		},
	}

	got, rewritten := cfg.RewriteModelForAPIKey("sk-user", "tenant-a/gpt-5.6")
	if !rewritten || got != "gpt-5.4" {
		t.Fatalf("RewriteModelForAPIKey() = %q/%v, want gpt-5.4/true", got, rewritten)
	}
}

func TestRewriteModelForAPIKeyBypass(t *testing.T) {
	cfg := &SDKConfig{
		ModelRewrite: ModelRewriteConfig{
			Enabled: true,
			Rules: []ModelRewriteRule{
				{MatchModels: []string{"gpt-5.5"}, TargetModel: "gpt-5.4", BypassAPIKeys: []string{"sk-whitelist"}},
			},
		},
	}

	got, rewritten := cfg.RewriteModelForAPIKey("sk-whitelist", "gpt-5.5")
	if rewritten || got != "gpt-5.5" {
		t.Fatalf("RewriteModelForAPIKey() = %q/%v, want original/false", got, rewritten)
	}
}

func TestRewriteModelForAPIKeyGroupBypass(t *testing.T) {
	cfg := &SDKConfig{
		APIKeyGroups: []APIKeyGroup{{ID: "core-developers", APIKeys: []string{"sk-group-member"}}},
		ModelRewrite: ModelRewriteConfig{
			Enabled: true,
			Rules: []ModelRewriteRule{
				{
					MatchModels:  []string{"gpt-5.6-*"},
					TargetModel:  "gpt-5.5",
					BypassGroups: []string{"core-developers"},
				},
			},
		},
	}
	cfg.SanitizeAPIKeyGroups()
	cfg.SanitizeModelRewrite()

	got, rewritten := cfg.RewriteModelForAPIKey("sk-group-member", "gpt-5.6-sol")
	if rewritten || got != "gpt-5.6-sol" {
		t.Fatalf("RewriteModelForAPIKey() = %q/%v, want original/false", got, rewritten)
	}
	got, rewritten = cfg.RewriteModelForAPIKey("sk-other", "gpt-5.6-sol")
	if !rewritten || got != "gpt-5.5" {
		t.Fatalf("RewriteModelForAPIKey() = %q/%v, want gpt-5.5/true", got, rewritten)
	}
}

func TestRewriteModelForAPIKeyPreservesSourceSuffix(t *testing.T) {
	cfg := &SDKConfig{
		ModelRewrite: ModelRewriteConfig{
			Enabled: true,
			Rules: []ModelRewriteRule{
				{MatchModels: []string{"gpt-5.5"}, TargetModel: "gpt-5.4"},
			},
		},
	}

	got, rewritten := cfg.RewriteModelForAPIKey("sk-user", "gpt-5.5(high)")
	if !rewritten || got != "gpt-5.4(high)" {
		t.Fatalf("RewriteModelForAPIKey() = %q/%v, want gpt-5.4(high)/true", got, rewritten)
	}
}

func TestRewriteModelForAPIKeyTargetSuffixWins(t *testing.T) {
	cfg := &SDKConfig{
		ModelRewrite: ModelRewriteConfig{
			Enabled: true,
			Rules: []ModelRewriteRule{
				{MatchModels: []string{"gpt-5.5"}, TargetModel: "gpt-5.4(low)"},
			},
		},
	}

	got, rewritten := cfg.RewriteModelForAPIKey("sk-user", "gpt-5.5(high)")
	if !rewritten || got != "gpt-5.4(low)" {
		t.Fatalf("RewriteModelForAPIKey() = %q/%v, want gpt-5.4(low)/true", got, rewritten)
	}
}

func TestRewriteModelForAPIKeyTargetThinkingEffortWins(t *testing.T) {
	cfg := &SDKConfig{
		ModelRewrite: ModelRewriteConfig{
			Enabled: true,
			Rules: []ModelRewriteRule{
				{MatchModels: []string{"gpt-5.6-sol"}, TargetModel: "gpt-5.5", TargetThinkingEffort: "high"},
			},
		},
	}

	result, rewritten := cfg.RewriteModelForAPIKeyWithOptions("sk-user", "gpt-5.6-sol(ultra)")
	if !rewritten || result.Model != "gpt-5.5(high)" || result.ThinkingEffort != "high" {
		t.Fatalf("RewriteModelForAPIKeyWithOptions() = %+v/%v, want gpt-5.5(high)/high/true", result, rewritten)
	}
}
