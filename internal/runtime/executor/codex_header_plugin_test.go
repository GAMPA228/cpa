package executor

import (
	"context"
	"net/http"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type testCodexHeaderHost struct {
	prepared  pluginapi.CodexHeaderRequest
	observed  pluginapi.CodexHeaderObservation
	completed pluginapi.CodexHeaderCompletion
	response  pluginapi.CodexHeaderResponse
}

func (h *testCodexHeaderHost) HasCodexHeaderPlugin() bool { return true }
func (h *testCodexHeaderHost) PrepareCodexHeaders(_ context.Context, req pluginapi.CodexHeaderRequest) (pluginapi.CodexHeaderResponse, error) {
	h.prepared = req
	return h.response, nil
}
func (h *testCodexHeaderHost) ObserveCodexHeaders(_ context.Context, req pluginapi.CodexHeaderObservation) error {
	h.observed = req
	return nil
}
func (h *testCodexHeaderHost) CompleteCodexHeaders(_ context.Context, req pluginapi.CodexHeaderCompletion) error {
	h.completed = req
	return nil
}

func TestCodexHeaderPluginUsesSelectedAuthAndFinalHeaders(t *testing.T) {
	host := &testCodexHeaderHost{response: pluginapi.CodexHeaderResponse{
		Headers:      http.Header{"X-Account": {"updated"}, "Cookie": {"session=private"}},
		ClearHeaders: []string{"X-Old"}, Signature: "account-version", ReservationID: "reservation-1",
	}}
	auth := &cliproxyauth.Auth{ID: "account-one", Provider: "codex", Metadata: map[string]any{
		"request_header_rules": []any{map[string]any{"name": "X-Account", "operation": "override", "value": "updated"}},
	}}
	headers := http.Header{"Authorization": {"Bearer upstream"}, "X-Old": {"stale"}}
	key, refresh, err := prepareCodexUpstreamHeaders(context.Background(),
		cliproxyexecutor.Options{CodexHeaderHost: host}, headers, auth, "gpt-5.6", "https://example.com/responses")
	if err != nil {
		t.Fatal(err)
	}
	if key != "account-version" || headers.Get("X-Old") != "" || headers.Get("X-Account") != "updated" ||
		headers.Get("Authorization") != "Bearer upstream" || host.prepared.Headers.Get("Authorization") != "" ||
		host.prepared.AuthID != auth.ID || host.prepared.Model != "gpt-5.6" {
		t.Fatalf("plugin header result mismatch: key=%q headers=%v prepared=%#v", key, headers, host.prepared)
	}
	refresh.Finish(false)
	if host.completed.ReservationID != "reservation-1" || host.completed.Attempted {
		t.Fatalf("refresh reservation not released: %#v", host.completed)
	}
}

func TestCodexHeaderPluginCannotReplaceAuthorization(t *testing.T) {
	host := &testCodexHeaderHost{response: pluginapi.CodexHeaderResponse{
		Headers: http.Header{"Authorization": {"Bearer fake"}},
	}}
	_, _, err := prepareCodexUpstreamHeaders(context.Background(),
		cliproxyexecutor.Options{CodexHeaderHost: host}, http.Header{},
		&cliproxyauth.Auth{ID: "account", Provider: "codex"}, "model", "https://example.com/responses")
	if err == nil {
		t.Fatal("plugin was allowed to replace upstream Authorization")
	}
}
