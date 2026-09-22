package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/turnstate"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

type codexHeaderRefresh interface{ Finish(bool) }
type codexNoopRefresh struct{}

func (codexNoopRefresh) Finish(bool) {}

type pluginHeaderRefresh struct {
	ctx  context.Context
	host cliproxyexecutor.CodexHeaderHost
	id   string
}

func (r *pluginHeaderRefresh) Finish(attempted bool) {
	if r == nil || r.id == "" {
		return
	}
	// Completion is needed even if the client disconnected after the request.
	if err := r.host.CompleteCodexHeaders(context.WithoutCancel(r.ctx), pluginapi.CodexHeaderCompletion{
		ReservationID: r.id, Attempted: attempted,
	}); err != nil {
		log.Warn("codex header plugin could not complete refresh reservation")
	}
}

func prepareCodexUpstreamHeaders(ctx context.Context, opts cliproxyexecutor.Options, headers http.Header, auth *cliproxyauth.Auth, model, upstreamURL string) (string, codexHeaderRefresh, error) {
	host := opts.CodexHeaderHost
	if host == nil || !host.HasCodexHeaderPlugin() || auth == nil || !strings.EqualFold(auth.Provider, "codex") {
		return "", codexNoopRefresh{}, nil
	}
	rules, err := json.Marshal(auth.Metadata["request_header_rules"])
	if err != nil {
		return "", nil, fmt.Errorf("encode codex header rules: %w", err)
	}
	pluginHeaders := headers.Clone()
	for name := range pluginHeaders {
		if prohibitedPluginHeader(name) || strings.EqualFold(name, "x-api-key") || strings.EqualFold(name, "api-key") {
			delete(pluginHeaders, name)
		}
	}
	resp, err := host.PrepareCodexHeaders(ctx, pluginapi.CodexHeaderRequest{
		AuthID: auth.ID, OAuth: auth.AuthKind() == cliproxyauth.AuthKindOAuth, Model: model, URL: upstreamURL, Rules: rules, Headers: pluginHeaders,
	})
	if err != nil {
		return "", nil, fmt.Errorf("prepare codex plugin headers: %w", err)
	}
	for _, name := range resp.ClearHeaders {
		if prohibitedPluginHeader(name) {
			return "", nil, fmt.Errorf("codex header plugin attempted to clear protected header")
		}
		for actual := range headers {
			if strings.EqualFold(actual, name) {
				delete(headers, actual)
			}
		}
	}
	for name, values := range resp.Headers {
		if prohibitedPluginHeader(name) {
			return "", nil, fmt.Errorf("codex header plugin attempted to set protected header")
		}
		headers.Del(name)
		for _, value := range values {
			headers.Add(name, value)
		}
	}
	if resp.ReservationID == "" {
		return resp.Signature, codexNoopRefresh{}, nil
	}
	return resp.Signature, &pluginHeaderRefresh{ctx: ctx, host: host, id: resp.ReservationID}, nil
}

func prohibitedPluginHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "authorization", "host", "proxy-authorization", "connection", "upgrade", "content-length", "transfer-encoding", "te", "trailer":
		return true
	}
	return false
}

// Observe all raw handshake/HTTP headers, including error responses; only the
// plugin decides which responses are eligible for Turn State maintenance.
func observeCodexUpstreamHeaders(ctx context.Context, opts cliproxyexecutor.Options, reporter *helps.UsageReporter, auth *cliproxyauth.Auth, model, upstreamURL string, response *http.Response) {
	if response == nil {
		return
	}
	_, length := turnstate.Value(response.Header)
	reporter.SetTurnStateLength(length)
	host := opts.CodexHeaderHost
	if host == nil || !host.HasCodexHeaderPlugin() || auth == nil || !strings.EqualFold(auth.Provider, "codex") {
		return
	}
	if err := host.ObserveCodexHeaders(ctx, pluginapi.CodexHeaderObservation{
		AuthID: auth.ID, OAuth: auth.AuthKind() == cliproxyauth.AuthKindOAuth, Model: model, URL: upstreamURL, StatusCode: response.StatusCode, Headers: response.Header.Clone(),
	}); err != nil {
		log.Warn("codex header plugin could not observe upstream response")
	}
}

func observeCodexTurnStateEvent(ctx context.Context, opts cliproxyexecutor.Options, reporter *helps.UsageReporter, auth *cliproxyauth.Auth, model, upstreamURL string, payload []byte) {
	headers := helps.CodexTurnStateEventHeaders(payload)
	if len(headers) > 0 {
		observeCodexUpstreamHeaders(ctx, opts, reporter, auth, model, upstreamURL, &http.Response{Header: headers, StatusCode: http.StatusOK})
	}
}
