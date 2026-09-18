package helps

import (
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
	rules := authheaders.Select(codexAccountHeaderRules(auth), name, time.Now())
	if turnStateAccount(auth) {
		if rule, ok := turnstate.Default.Lookup(auth.ID, name); ok {
			manual := false
			for _, existing := range rules {
				if strings.EqualFold(existing.Name, turnstate.Header) {
					manual = true
					break
				}
			}
			if !manual {
				rules = append(rules, authheaders.Rule{Name: turnstate.Header, Operation: "override", Value: rule.Value})
			}
		}
	}
	authheaders.Apply(headers, rules)
	return authheaders.Signature(rules)
}

// CodexAccountHeaderRulesKey invalidates reusable connections when account rules change.
func CodexAccountHeaderRulesKey(auth *cliproxyauth.Auth) string {
	return authheaders.Signature(authheaders.Select(codexAccountHeaderRules(auth), "", time.Now()))
}
