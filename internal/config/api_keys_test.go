package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseConfigBytesAcceptsAPIKeyEntryObjects(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
api-keys:
  - " sk-legacy "
  - api-key: " sk-structured "
    remark: " Alice "
    daily-token-limit: 100000000
  - key: " sk-alias "
    name: " Bob "
    daily_limit: "200000000"
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}

	wantKeys := []string{"sk-legacy", "sk-structured", "sk-alias"}
	if len(cfg.APIKeys) != len(wantKeys) {
		t.Fatalf("APIKeys = %#v, want %#v", cfg.APIKeys, wantKeys)
	}
	for i := range wantKeys {
		if cfg.APIKeys[i] != wantKeys[i] {
			t.Fatalf("APIKeys[%d] = %q, want %q", i, cfg.APIKeys[i], wantKeys[i])
		}
	}
	if got := cfg.APIKeyRemarkMap()["sk-structured"]; got != "Alice" {
		t.Fatalf("structured remark = %q, want Alice", got)
	}
	if got := cfg.APIKeyRemarkMap()["sk-alias"]; got != "Bob" {
		t.Fatalf("alias remark = %q, want Bob", got)
	}
	limits := cfg.APIKeyDailyTokenLimitMap()
	if got := limits["sk-structured"]; got != 100000000 {
		t.Fatalf("structured daily token limit = %d, want 100000000", got)
	}
	if got := limits["sk-alias"]; got != 200000000 {
		t.Fatalf("alias daily token limit = %d, want 200000000", got)
	}
}

func TestAPIKeyEntryListMarshalUsesStringsWithoutRemarks(t *testing.T) {
	cfg := &Config{}
	cfg.APIKeys = []string{" sk-a ", "sk-b"}
	cfg.SanitizeAPIKeyEntries()

	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "- sk-a") || strings.Contains(text, "api-key: sk-a") || strings.Contains(text, "api-key: sk-b") {
		t.Fatalf("unexpected api-keys YAML:\n%s", text)
	}
}

func TestAPIKeyEntryListMarshalUsesObjectsWithDailyTokenLimit(t *testing.T) {
	cfg := &Config{}
	cfg.APIKeyEntries = APIKeyEntryList{
		{APIKey: "sk-a", DailyTokenLimit: 100000000},
		{APIKey: "sk-b"},
	}
	cfg.SanitizeAPIKeyEntries()

	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "api-key: sk-a") || !strings.Contains(text, "daily-token-limit: 100000000") {
		t.Fatalf("unexpected api-keys YAML:\n%s", text)
	}
}

func TestSaveConfigPreserveCommentsKeepsAPIKeyRemarks(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	initial := []byte(`
api-keys:
  - api-key: sk-a
    remark: Alice
    daily-token-limit: 100000000
  - sk-b
`)
	if err := os.WriteFile(configPath, initial, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if err := SaveConfigPreserveComments(configPath, cfg); err != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "api-key: sk-a") ||
		!strings.Contains(text, "remark: Alice") ||
		!strings.Contains(text, "daily-token-limit: 100000000") {
		t.Fatalf("saved config lost api key metadata:\n%s", text)
	}
}
