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
					MatchModels:   []string{" GPT-5.5 ", "gpt-5.5"},
					TargetModel:   " gpt-5.4 ",
					BypassAPIKeys: []string{" sk-a ", "sk-a", "SK-A"},
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
	if got := rule.MatchModels; len(got) != 1 || got[0] != "GPT-5.5" {
		t.Fatalf("match models = %#v, want trimmed case-insensitive dedupe", got)
	}
	if got := rule.BypassAPIKeys; len(got) != 2 || got[0] != "sk-a" || got[1] != "SK-A" {
		t.Fatalf("bypass api keys = %#v, want case-sensitive dedupe", got)
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
