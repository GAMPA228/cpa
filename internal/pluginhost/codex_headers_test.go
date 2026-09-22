package pluginhost

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type testHeaderCapability struct{ panicOnPrepare bool }

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
