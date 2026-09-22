package executor

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/turnstate"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type testCodexHeaderHost struct {
	disabled  bool
	prepared  pluginapi.CodexHeaderRequest
	observed  pluginapi.CodexHeaderObservation
	completed pluginapi.CodexHeaderCompletion
	response  pluginapi.CodexHeaderResponse
}

func (h *testCodexHeaderHost) HasCodexHeaderPlugin() bool { return !h.disabled }
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

func TestCodexHeaderPluginAbsentDoesNotMutateHeaders(t *testing.T) {
	for _, mode := range []string{"unloaded", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			_, _, _ = refreshTestManager(t, t.Name())
			host := &testCodexHeaderHost{disabled: true}
			opts := cliproxyexecutor.Options{}
			if mode == "disabled" {
				opts.CodexHeaderHost = host
			}
			auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "codex", Metadata: map[string]any{
				"access_token": "test", "request_header_rules": []any{map[string]any{"name": "X-Test", "operation": "override", "value": "native"}},
			}}
			headers := http.Header{"X-Test": {"original"}, "Cookie": {"client=original"}, "x-codex-turn-state": {"client-state"}}
			before := headers.Clone()
			key, refresh, err := prepareCodexUpstreamHeaders(context.Background(), opts, headers, auth, "gpt-5.4", "https://example.com/responses")
			if err != nil || key != "" || !reflect.DeepEqual(headers, before) {
				t.Fatalf("absent plugin mutated headers: key=%q headers=%v err=%v", key, headers, err)
			}
			if refresh == nil {
				t.Fatal("missing no-op completion")
			}
			refresh.Finish(true)
			if host.prepared.AuthID != "" || host.completed.ReservationID != "" {
				t.Fatal("disabled host called")
			}
		})
	}
}

func TestCodexPrepareRequestDoesNotApplyAccountRules(t *testing.T) {
	auth := &cliproxyauth.Auth{ID: "account", Provider: "codex", Metadata: map[string]any{
		"access_token":         "upstream-token",
		"request_header_rules": []any{map[string]any{"name": "X-Test", "operation": "override", "value": "native"}},
	}}
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Test", "original")
	if err := NewCodexExecutor(&config.Config{}).PrepareRequest(req, auth); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("X-Test") != "original" || req.Header.Get("Authorization") != "Bearer upstream-token" {
		t.Fatalf("account rules applied or credentials lost: %v", req.Header)
	}
}

func TestCodexHeaderObservationStatsOnly(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		headers http.Header
		event   []byte
		want    int
	}{
		{name: "success", status: 200, headers: http.Header{turnstate.Header: {"token"}}, want: 5},
		{name: "error", status: 503, headers: http.Header{turnstate.Header: {"token"}}, want: 5},
		{name: "handshake", status: 101, headers: http.Header{turnstate.Header: {"token"}}, want: 5},
		{name: "reused-handshake", want: -1},
		{name: "duplicates", status: 200, headers: http.Header{turnstate.Header: {"one", "two"}}, want: -1},
		{name: "metadata", event: []byte(`{"type":"codex.response.metadata","headers":{"x-codex-turn-state":"token"}}`), want: 5},
		{name: "ordinary-event", event: []byte(`{"type":"response.output_text.delta","headers":{"X-Codex-Turn-State":"not-a-header"}}`), want: -1},
		{name: "nonstring-event", event: []byte(`{"type":"codex.response.metadata","headers":{"X-Codex-Turn-State":42}}`), want: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "codex"}
			sink := &responseModelUsageSink{authID: auth.ID, records: make(chan usage.Record, 1)}
			usage.RegisterPlugin(sink)
			reporter := helps.NewUsageReporter(context.Background(), "codex", "model", auth)
			if tc.event != nil {
				observeCodexTurnStateEvent(context.Background(), cliproxyexecutor.Options{}, reporter, auth, "model", "https://example.com/responses", tc.event)
			} else {
				var response *http.Response
				if tc.status != 0 {
					response = &http.Response{StatusCode: tc.status, Header: tc.headers}
				}
				observeCodexUpstreamHeaders(context.Background(), cliproxyexecutor.Options{}, reporter, auth, "model", "https://example.com/responses", response)
			}
			reporter.Publish(context.Background(), usage.Detail{InputTokens: 1})
			select {
			case record := <-sink.records:
				length := record.TurnStateLength
				if tc.want < 0 && length != nil || tc.want >= 0 && (length == nil || *length != tc.want) {
					t.Fatalf("length = %v, want %d", length, tc.want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("usage not published")
			}
		})
	}
}
