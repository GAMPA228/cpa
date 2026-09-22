package pluginhost

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type testHeaderCapability struct{ panicOnPrepare bool }

func TestDisableHeaderPluginQuiescesAndCanReenable(t *testing.T) {
	for _, global := range []bool{false, true} {
		t.Run(fmt.Sprintf("global=%t", global), func(t *testing.T) {
			quiesces := 0
			entry := validTestPlugin("alpha")
			entry.Capabilities.CodexHeaderPlugin = testHeaderCapability{}
			client := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
				switch method {
				case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
					return lifecycleRegistrationResult(entry)
				case pluginabi.MethodPluginQuiesce:
					quiesces++
					return marshalRPCResult(rpcEmptyResponse{})
				default:
					return nil, fmt.Errorf("unexpected method %s", method)
				}
			}}
			h := NewForTest(&sequencePluginLoader{clients: []pluginClient{client}})
			t.Cleanup(h.ShutdownAll)
			cfg := &config.Config{Plugins: config.PluginsConfig{Enabled: true, Dir: makePluginDir(t, "alpha"), Configs: enabledPluginConfigs("alpha")}}
			h.ApplyConfig(context.Background(), cfg)
			if !h.HasCodexHeaderPlugin() {
				t.Fatal("plugin not active")
			}
			if global {
				cfg.Plugins.Enabled = false
			} else {
				disabled := false
				cfg.Plugins.Configs["alpha"] = config.PluginInstanceConfig{Enabled: &disabled}
			}
			h.ApplyConfig(context.Background(), cfg)
			if h.HasCodexHeaderPlugin() || quiesces != 1 {
				t.Fatalf("disable: active=%v quiesces=%d", h.HasCodexHeaderPlugin(), quiesces)
			}
			h.ApplyConfig(context.Background(), cfg)
			if quiesces != 1 {
				t.Fatal("already disabled plugin quiesced twice")
			}
			cfg.Plugins.Enabled = true
			cfg.Plugins.Configs = enabledPluginConfigs("alpha")
			h.ApplyConfig(context.Background(), cfg)
			if !h.HasCodexHeaderPlugin() {
				t.Fatal("plugin did not re-enable")
			}
		})
	}
}

func (h testHeaderCapability) PrepareCodexHeaders(_ context.Context, req pluginapi.CodexHeaderRequest) (pluginapi.CodexHeaderResponse, error) {
	if h.panicOnPrepare {
		panic("test failure")
	}
	return pluginapi.CodexHeaderResponse{Headers: http.Header{"X-Account": {req.AuthID}}}, nil
}
func (testHeaderCapability) ObserveCodexHeaders(context.Context, pluginapi.CodexHeaderObservation) error {
	return nil
}
func (testHeaderCapability) CompleteCodexHeaders(context.Context, pluginapi.CodexHeaderCompletion) error {
	return nil
}

func TestCodexHeaderPluginCapability(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "headers", plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{CodexHeaderPlugin: testHeaderCapability{}},
		},
	})
	if !host.HasCodexHeaderPlugin() {
		t.Fatal("active header plugin not detected")
	}
	resp, err := host.PrepareCodexHeaders(context.Background(), pluginapi.CodexHeaderRequest{AuthID: "account"})
	if err != nil || resp.Headers.Get("X-Account") != "account" {
		t.Fatalf("prepare = %#v, %v", resp, err)
	}
	ambiguous := newHostWithRecords(
		capabilityRecord{id: "first", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{CodexHeaderPlugin: testHeaderCapability{}}}},
		capabilityRecord{id: "second", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{CodexHeaderPlugin: testHeaderCapability{}}}},
	)
	if ambiguous.HasCodexHeaderPlugin() {
		t.Fatal("ambiguous header plugins must not be active")
	}
}

func TestCodexHeaderPluginPanicFusesPlugin(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "headers", plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{CodexHeaderPlugin: testHeaderCapability{panicOnPrepare: true}},
		},
	})
	if _, err := host.PrepareCodexHeaders(context.Background(), pluginapi.CodexHeaderRequest{}); err == nil {
		t.Fatal("plugin panic should return an error")
	}
	if host.HasCodexHeaderPlugin() {
		t.Fatal("panicking plugin should be fused")
	}
}

func TestCodexHeaderPluginRPC(t *testing.T) {
	entry := validTestPlugin("codex headers")
	entry.Capabilities.CodexHeaderPlugin = testHeaderCapability{}
	lookup := newTestSymbolLookup(&testPlugin{registerResult: entry})
	registered, err := registerRPCPlugin(context.Background(), nil, "codex-headers", lookup,
		"plugin.register", nil)
	if err != nil || registered.Capabilities.CodexHeaderPlugin == nil {
		t.Fatalf("plugin capability not registered: %#v %v", registered, err)
	}
	resp, err := registered.Capabilities.CodexHeaderPlugin.PrepareCodexHeaders(context.Background(),
		pluginapi.CodexHeaderRequest{AuthID: "one", Headers: http.Header{"X-Old": {"1"}}})
	if err != nil || resp.Headers.Get("X-Account") != "one" {
		t.Fatalf("RPC prepare = %#v, %v", resp, err)
	}
	if err := registered.Capabilities.CodexHeaderPlugin.ObserveCodexHeaders(context.Background(),
		pluginapi.CodexHeaderObservation{AuthID: "one"}); err != nil {
		t.Fatal(err)
	}
	if err := registered.Capabilities.CodexHeaderPlugin.CompleteCodexHeaders(context.Background(),
		pluginapi.CodexHeaderCompletion{ReservationID: "one"}); err != nil {
		t.Fatal(err)
	}
}
