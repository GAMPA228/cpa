package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigNormalizesManagementUIProxyNodesURL(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	content := "management-ui:\n  proxy-nodes-url: '  https://nodes.example.com/panel?team=dev  '\n"
	if errWrite := os.WriteFile(configPath, []byte(content), 0o600); errWrite != nil {
		t.Fatalf("write config: %v", errWrite)
	}

	cfg, errLoad := LoadConfig(configPath)
	if errLoad != nil {
		t.Fatalf("LoadConfig() error = %v", errLoad)
	}
	if got, want := cfg.ManagementUI.ProxyNodesURL, "https://nodes.example.com/panel?team=dev"; got != want {
		t.Fatalf("ProxyNodesURL = %q, want %q", got, want)
	}

	encoded, errMarshal := json.Marshal(cfg)
	if errMarshal != nil {
		t.Fatalf("json.Marshal() error = %v", errMarshal)
	}
	if !strings.Contains(string(encoded), `"management-ui":{"proxy-nodes-url":"https://nodes.example.com/panel?team=dev"}`) {
		t.Fatalf("management UI config missing from JSON: %s", encoded)
	}
}

func TestLoadConfigRejectsInvalidManagementUIProxyNodesURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{name: "relative", url: "/proxy-nodes"},
		{name: "unsupported scheme", url: "javascript:alert(1)"},
		{name: "credentials", url: "https://user:password@nodes.example.com"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			content := fmt.Sprintf("management-ui:\n  proxy-nodes-url: %q\n", testCase.url)
			if errWrite := os.WriteFile(configPath, []byte(content), 0o600); errWrite != nil {
				t.Fatalf("write config: %v", errWrite)
			}

			_, errLoad := LoadConfig(configPath)
			if errLoad == nil || !strings.Contains(errLoad.Error(), "management-ui.proxy-nodes-url") {
				t.Fatalf("LoadConfig() error = %v, want proxy nodes URL validation error", errLoad)
			}
		})
	}
}
