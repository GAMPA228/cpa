package helps

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/turnstate"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestCodexCookieJarIsAccountScoped(t *testing.T) {
	old := turnstate.Default
	path := filepath.Join(t.TempDir(), "state.db")
	m := turnstate.NewManager()
	turnstate.Default = m
	t.Cleanup(func() { m.Close(); turnstate.Default = old })
	if err := m.Open(path); err != nil {
		t.Fatal(err)
	}
	if err := m.Configure(turnstate.Settings{Enabled: true, MaxChars: 292}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/set" {
			http.SetCookie(w, &http.Cookie{Name: "test_session", Value: "account-a", Path: "/"})
		}
		_, _ = w.Write([]byte(r.Header.Get("Cookie")))
	}))
	defer server.Close()
	request := func(client *http.Client, path string) string {
		t.Helper()
		response, err := client.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		buf := make([]byte, 256)
		n, _ := response.Body.Read(buf)
		return string(buf[:n])
	}
	accountA := &cliproxyauth.Auth{ID: "cookie-a", Provider: "codex"}
	accountB := &cliproxyauth.Auth{ID: "cookie-b", Provider: "codex"}
	a := &http.Client{}
	attachCodexCookieJar(a, accountA)
	request(a, "/set")
	same := &http.Client{}
	attachCodexCookieJar(same, accountA)
	if got := request(same, "/check"); got != "test_session=account-a" {
		t.Fatalf("same account cookie = %q", got)
	}
	b := &http.Client{}
	attachCodexCookieJar(b, accountB)
	if got := request(b, "/check"); got != "" {
		t.Fatalf("another account received cookie = %q", got)
	}
	if err := m.Configure(turnstate.Settings{Enabled: false, MaxChars: 292}); err != nil {
		t.Fatal(err)
	}
	disabled := &http.Client{}
	attachCodexCookieJar(disabled, accountA)
	if got := request(disabled, "/check"); got != "" {
		t.Fatalf("disabled account received cookie = %q", got)
	}
}
