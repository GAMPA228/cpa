package helps

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/authheaders"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestCodexAccountHeadersIsolationAndRemoval(t *testing.T) {
	a := &cliproxyauth.Auth{Provider: "codex", Metadata: map[string]any{authheaders.MetadataKey: []authheaders.Rule{{Name: "User-Agent", Operation: "override", Value: "account-a"}}}}
	b := &cliproxyauth.Auth{Provider: "codex"}
	for _, auth := range []*cliproxyauth.Auth{a, b, a} {
		h := http.Header{"User-Agent": {"global"}}
		ApplyCodexAccountHeaders(h, auth)
		want := "global"
		if auth == a {
			want = "account-a"
		}
		if h.Get("User-Agent") != want {
			t.Fatal("account rules leaked")
		}
	}
	key := CodexAccountHeaderRulesKey(a)
	if key == "" || key == CodexAccountHeaderRulesKey(b) {
		t.Fatal("missing rules key")
	}
	b = a.Clone()
	b.Metadata[authheaders.MetadataKey] = []authheaders.Rule{{Name: "User-Agent", Operation: "override", Value: "account-b"}}
	if key == CodexAccountHeaderRulesKey(b) {
		t.Fatal("changed rules have same key")
	}
	delete(b.Metadata, authheaders.MetadataKey)
	if CodexAccountHeaderRulesKey(b) != "" {
		t.Fatal("removed rules key not cleared")
	}
	refreshed := &cliproxyauth.Auth{Provider: "codex", Metadata: map[string]any{"access_token": "new"}}
	cliproxyauth.MergeExistingAuthMetadata(refreshed, a.Metadata)
	if CodexAccountHeaderRulesKey(refreshed) != key {
		t.Fatal("relogin lost rules")
	}
}
