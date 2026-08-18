package helps

import "testing"

func TestResolveProxyRoutePrecedenceAndSanitization(t *testing.T) {
	tests := []struct {
		name       string
		auth       string
		global     string
		wantMode   string
		wantSource string
		wantProto  string
		wantHost   string
	}{
		{name: "auth proxy wins", auth: "socks5://user:secret@10.20.1.8:1080/private?q=secret", global: "http://global:8080", wantMode: "proxy", wantSource: "auth", wantProto: "socks5", wantHost: "10.20.1.8:1080"},
		{name: "global fallback", global: "https://proxy.example.com:8443/path", wantMode: "proxy", wantSource: "global", wantProto: "https", wantHost: "proxy.example.com:8443"},
		{name: "auth direct overrides global", auth: "direct", global: "http://global:8080", wantMode: "direct", wantSource: "auth"},
		{name: "no proxy", wantMode: "direct", wantSource: "none"},
		{name: "ipv6", auth: "socks5h://[2001:db8::7]:1080", wantMode: "proxy", wantSource: "auth", wantProto: "socks5h", wantHost: "[2001:db8::7]:1080"},
		{name: "invalid auth fails closed", auth: "not-a-proxy", global: "http://global:8080", wantMode: "unknown", wantSource: "auth"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveProxyRoute(tt.auth, tt.global)
			if got.mode != tt.wantMode || got.source != tt.wantSource || got.protocol != tt.wantProto || got.endpoint != tt.wantHost {
				t.Fatalf("resolveProxyRoute() = %#v", got)
			}
			if got.endpoint == "user:secret@10.20.1.8:1080" {
				t.Fatal("proxy endpoint leaked credentials")
			}
		})
	}
}
