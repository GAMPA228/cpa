package helps

import (
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/authheaders"
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
	authheaders.Apply(headers, rules)
	return authheaders.Signature(rules)
}

// CodexAccountHeaderRulesKey invalidates reusable connections when account rules change.
func CodexAccountHeaderRulesKey(auth *cliproxyauth.Auth) string {
	return authheaders.Signature(authheaders.Select(codexAccountHeaderRules(auth), "", time.Now()))
}
