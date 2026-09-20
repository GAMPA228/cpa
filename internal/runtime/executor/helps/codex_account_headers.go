package helps

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/authheaders"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/turnstate"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func codexAccountHeaderRules(auth *cliproxyauth.Auth) []authheaders.Rule {
	if auth == nil || !strings.EqualFold(auth.Provider, "codex") {
		return nil
	}
	rules, err := authheaders.Decode(auth.Metadata[authheaders.MetadataKey])
	if err != nil {
		return nil
	}
	return rules
}

// ApplyCodexAccountHeaders must run after default, model and identity headers.
func ApplyCodexAccountHeaders(headers http.Header, auth *cliproxyauth.Auth, model ...string) string {
	name := ""
	if len(model) > 0 {
		name = model[0]
	}
	key, _ := applyCodexAccountHeaders(headers, auth, name, false)
	return key
}

// PrepareCodexAccountHeaders is used by model executions. The caller must finish
// the returned reservation on every exit, or after the stream goroutine completes.
func PrepareCodexAccountHeaders(headers http.Header, auth *cliproxyauth.Auth, model string) (string, *turnstate.Refresh) {
	return applyCodexAccountHeaders(headers, auth, model, true)
}

func applyCodexAccountHeaders(headers http.Header, auth *cliproxyauth.Auth, model string, allowRefresh bool) (string, *turnstate.Refresh) {
	rules := authheaders.Select(codexAccountHeaderRules(auth), model, time.Now())
	var refresh *turnstate.Refresh
	if turnStateAccount(auth) {
		value, omit := "", false
		if allowRefresh {
			value, omit, refresh = turnstate.Default.PrepareRequest(auth.ID, model)
		} else if rule, ok := turnstate.Default.Lookup(auth.ID, model); ok {
			value = rule.Value
		}
		if omit || value != "" {
			// Remove every casing before mutation; raw WebSocket headers may not be canonical.
			for name := range headers {
				if strings.EqualFold(name, turnstate.Header) {
					delete(headers, name)
				}
			}
			operation := "override"
			if omit {
				operation = "delete"
			}
			rules = append(rules, authheaders.Rule{Name: turnstate.Header, Operation: operation, Value: value})
		}
	}
	authheaders.Apply(headers, rules)
	key := authheaders.Signature(rules)
	if refresh != nil {
		key += fmt.Sprintf(":turn-state-refresh:%d", refresh.ID())
	}
	return key, refresh
}

// CodexAccountHeaderRulesKey invalidates reusable connections when account rules change.
func CodexAccountHeaderRulesKey(auth *cliproxyauth.Auth) string {
	return authheaders.Signature(authheaders.Select(codexAccountHeaderRules(auth), "", time.Now()))
}
