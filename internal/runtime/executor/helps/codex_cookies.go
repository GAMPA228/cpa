package helps

import (
	"net/http"
	"net/http/cookiejar"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/turnstate"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"golang.org/x/net/publicsuffix"
)

var codexCookieJars sync.Map

// Codex cookies stay in memory and are never shared between upstream auth IDs.
func CodexAccountCookieJar(auth *cliproxyauth.Auth) http.CookieJar {
	if auth == nil || auth.Provider != "codex" || auth.ID == "" {
		return nil
	}
	if !turnstate.Default.AccountEnabled(auth.ID) {
		codexCookieJars.Delete(auth.ID)
		return nil
	}
	if existing, ok := codexCookieJars.Load(auth.ID); ok {
		return existing.(http.CookieJar)
	}
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil
	}
	actual, _ := codexCookieJars.LoadOrStore(auth.ID, jar)
	return actual.(http.CookieJar)
}

func attachCodexCookieJar(client *http.Client, auth *cliproxyauth.Auth) {
	client.Jar = CodexAccountCookieJar(auth)
}
