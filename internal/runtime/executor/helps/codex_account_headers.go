package helps

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

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
func ApplyCodexAccountHeaders(headers http.Header, auth *cliproxyauth.Auth) {
	authheaders.Apply(headers, codexAccountHeaderRules(auth))
}

// CodexAccountHeaderRulesKey invalidates reusable connections when account rules change.
func CodexAccountHeaderRulesKey(auth *cliproxyauth.Auth) string {
	rules := codexAccountHeaderRules(auth)
	if len(rules) == 0 {
		return ""
	}
	data, _ := json.Marshal(rules)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
