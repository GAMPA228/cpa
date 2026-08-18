package helps

import (
	"strings"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

type proxyRoute struct {
	mode     string
	source   string
	protocol string
	endpoint string
}

func authProxyURL(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	return auth.ProxyURL
}

func resolveProxyRoute(authProxyURL, globalProxyURL string) proxyRoute {
	if route, inherited := parseProxyRoute(authProxyURL, "auth"); !inherited {
		return route
	}
	if route, inherited := parseProxyRoute(globalProxyURL, "global"); !inherited {
		return route
	}
	return proxyRoute{mode: "direct", source: "none"}
}

func parseProxyRoute(raw, source string) (proxyRoute, bool) {
	setting, errParse := proxyutil.Parse(raw)
	if errParse != nil || setting.Mode == proxyutil.ModeInvalid {
		return proxyRoute{mode: "unknown", source: source}, false
	}
	switch setting.Mode {
	case proxyutil.ModeInherit:
		return proxyRoute{}, true
	case proxyutil.ModeDirect:
		return proxyRoute{mode: "direct", source: source}, false
	case proxyutil.ModeProxy:
		if setting.URL == nil {
			return proxyRoute{mode: "unknown", source: source}, false
		}
		return proxyRoute{
			mode:     "proxy",
			source:   source,
			protocol: strings.ToLower(strings.TrimSpace(setting.URL.Scheme)),
			endpoint: strings.TrimSpace(setting.URL.Host),
		}, false
	default:
		return proxyRoute{mode: "unknown", source: source}, false
	}
}
