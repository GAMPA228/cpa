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
  - key: " sk-alias "
    name: " Bob "
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

func TestSaveConfigPreserveCommentsKeepsAPIKeyRemarks(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	initial := []byte(`
api-keys:
  - api-key: sk-a
    remark: Alice
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
	if !strings.Contains(text, "api-key: sk-a") || !strings.Contains(text, "remark: Alice") {
		t.Fatalf("saved config lost api key remark:\n%s", text)
	}
}
