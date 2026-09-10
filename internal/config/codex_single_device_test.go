package config

import (
	"gopkg.in/yaml.v3"
	"testing"
)

func TestCodexSingleDeviceConfigCompatibility(t *testing.T) {
	for _, item := range []struct {
		raw          string
		set, enabled bool
	}{
		{"codex: {identity-confuse: true}", false, false},
		{"codex: {single-device: true}", true, true},
		{"codex: {single-device: false, identity-confuse: true}", true, false},
	} {
		var cfg Config
		if err := yaml.Unmarshal([]byte(item.raw), &cfg); err != nil {
			t.Fatal(err)
		}
		if (cfg.Codex.SingleDevice != nil) != item.set {
			t.Fatal("missing field no longer preserves legacy behavior")
		}
		if item.set && *cfg.Codex.SingleDevice != item.enabled {
			t.Fatal("explicit value lost")
		}
		raw, err := yaml.Marshal(&cfg)
		if err != nil {
			t.Fatal(err)
		}
		var restored Config
		if err := yaml.Unmarshal(raw, &restored); err != nil {
			t.Fatal(err)
		}
		if (restored.Codex.SingleDevice != nil) != item.set || (item.set && *restored.Codex.SingleDevice != item.enabled) {
			t.Fatal("round-trip lost off or missing value")
		}
	}
}
