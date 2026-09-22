package helps

import (
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/turnstate"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func turnStateAccount(auth *cliproxyauth.Auth) bool {
	return auth != nil && strings.EqualFold(auth.Provider, "codex") && auth.AuthKind() == cliproxyauth.AuthKindOAuth && auth.ID != ""
}

// ObserveCodexTurnState records actual response headers, even when automation is disabled.
// A nil handshake means a reused WebSocket and must not reuse its old header snapshot.
func ObserveCodexTurnState(reporter *UsageReporter, auth *cliproxyauth.Auth, model string, response *http.Response) {
	if response == nil {
		return
	}
	value, length := turnstate.Value(response.Header)
	reporter.SetTurnStateLength(length)
	if length != nil && turnStateAccount(auth) && (response.StatusCode == http.StatusSwitchingProtocols || response.StatusCode >= 200 && response.StatusCode < 300) {
		turnstate.Default.Observe(auth.ID, model, value)
	}
}

// Native Codex sockets can return fresh response headers in a metadata event.
func ObserveCodexTurnStateEvent(reporter *UsageReporter, auth *cliproxyauth.Auth, model string, payload []byte) {
	headers := CodexTurnStateEventHeaders(payload)
	if len(headers) > 0 {
		ObserveCodexTurnState(reporter, auth, model, &http.Response{StatusCode: http.StatusOK, Header: headers})
	}
}

func CodexTurnStateEventHeaders(payload []byte) http.Header {
	if gjson.GetBytes(payload, "type").String() != "codex.response.metadata" {
		return nil
	}
	headers := make(http.Header)
	gjson.GetBytes(payload, "headers").ForEach(func(name, value gjson.Result) bool {
		if strings.EqualFold(name.String(), turnstate.Header) {
			if value.Type == gjson.String {
				headers.Add(turnstate.Header, value.String())
			}
			if value.IsArray() {
				for _, item := range value.Array() {
					if item.Type == gjson.String {
						headers.Add(turnstate.Header, item.String())
					}
				}
			}
		}
		return true
	})
	return headers
}
