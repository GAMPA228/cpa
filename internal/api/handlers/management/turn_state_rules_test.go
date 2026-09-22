package management

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/authheaders"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/turnstate"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestRequestHeaderRulesExcludeNativeAutomaticRules(t *testing.T) {
	old := turnstate.Default
	m := turnstate.NewManager()
	turnstate.Default = m
	defer func() { m.Close(); turnstate.Default = old }()
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	if err := m.Open(path); err != nil {
		t.Fatal(err)
	}
	if err := m.Configure(turnstate.Settings{Enabled: true, MaxChars: 292}); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 217)
	data[0] = 0x80
	binary.BigEndian.PutUint64(data[1:9], uint64(time.Now().Unix()))
	m.Observe("account-a", "native-only-model", base64.URLEncoding.EncodeToString(data))
	m.Close()
	if err := m.Open(path); err != nil {
		t.Fatal(err)
	}
	if len(m.Rules("account-a")) != 1 {
		t.Fatal("native rule fixture missing")
	}

	manual := []authheaders.Rule{{ID: "manual", Name: "Version", Operation: "override", Value: "1"}}
	store := &failingHeaderStore{}
	manager := coreauth.NewManager(store, nil, nil)
	_, err := manager.Register(context.Background(), &coreauth.Auth{
		ID: "account-a", FileName: "a.json", Provider: "codex",
		Metadata: map[string]any{authheaders.MetadataKey: manual},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	h.SetPluginHost(activeManagementHeaderHost())
	verify := func(w *httptest.ResponseRecorder, mutation bool) {
		t.Helper()
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		if bytes.Contains(w.Body.Bytes(), []byte("native-only-model")) || bytes.Contains(w.Body.Bytes(), []byte("turn-state-auto")) {
			t.Fatalf("native automatic rule leaked: %s", w.Body.String())
		}
		var out struct {
			Accounts []headerRuleAccount
			Account  headerRuleAccount
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		account := out.Account
		if !mutation {
			if len(out.Accounts) != 1 {
				t.Fatal("missing manual account")
			}
			account = out.Accounts[0]
		}
		if len(account.Rules) != 1 || account.Rules[0].ID != "manual" {
			t.Fatal("manual rule missing")
		}
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	h.GetRequestHeaderRules(c)
	verify(w, false)
	input := headerRuleMutation{AuthID: "account-a", Revision: headerRulesRevision(manual), Action: "save", ID: "manual", Rule: headerRuleDraft{Name: "Version", Operation: "override", Value: "2"}}
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	h.MutateRequestHeaderRules(c)
	verify(w, true)
	if len(m.Rules("account-a")) != 1 {
		t.Fatal("native history removed")
	}
	for _, action := range []string{"delete", "save", "restart"} {
		if _, err := mutateHeaderRules(manual, headerRuleMutation{ID: m.Rules("account-a")[0].ID(), Action: action}, time.Now()); err == nil {
			t.Fatal("automatic rule writable through manual mutation")
		}
	}
	if _, err := authheaders.Decode([]authheaders.Rule{{Name: turnstate.Header, Operation: "override", Value: "unsafe"}}); err == nil {
		t.Fatal("protected header became editable")
	}
}
