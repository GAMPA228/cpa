package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/authheaders"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHeaderRuleMutationDeadlines(t *testing.T) {
	now := time.Date(2026, 9, 17, 14, 0, 0, 0, time.UTC)
	input := headerRuleMutation{Action: "save", Rule: headerRuleDraft{Name: "Version", Operation: "override", Value: "a", DurationMinutes: 20}}
	rules, err := mutateHeaderRules(nil, input, now)
	if err != nil {
		t.Fatal(err)
	}
	id := rules[0].ID
	deadline := *rules[0].ExpiresAt
	if !deadline.Equal(now.Add(20 * time.Minute)) {
		t.Fatal("wrong initial deadline")
	}
	input.ID = id
	input.Rule.Value = "edited"
	rules, err = mutateHeaderRules(rules, input, now.Add(30*time.Minute))
	if err != nil || !rules[0].ExpiresAt.Equal(deadline) {
		t.Fatal("editing expired rule renewed it")
	}
	other := headerRuleMutation{Action: "save", Rule: headerRuleDraft{Name: "User-Agent", Operation: "override", Value: "other", DurationMinutes: 10}}
	rules, err = mutateHeaderRules(rules, other, now.Add(40*time.Minute))
	if err != nil || !rules[0].ExpiresAt.Equal(deadline) {
		t.Fatal("editing another rule renewed deadline")
	}
	rules, err = mutateHeaderRules(rules, headerRuleMutation{Action: "restart", ID: id}, now.Add(time.Hour))
	if err != nil || !rules[0].ExpiresAt.Equal(now.Add(80*time.Minute)) {
		t.Fatal("explicit restart failed")
	}
	input.Rule.DurationMinutes = 30
	rules, err = mutateHeaderRules(rules, input, now.Add(2*time.Hour))
	if err != nil || !rules[0].ExpiresAt.Equal(now.Add(150*time.Minute)) {
		t.Fatal("duration change did not reset deadline")
	}
	input.Rule.DurationMinutes = 0
	rules, err = mutateHeaderRules(rules, input, now)
	if err != nil || rules[0].ExpiresAt != nil {
		t.Fatal("permanent did not clear deadline")
	}
	keep := rules[1]
	rules, err = mutateHeaderRules(rules, headerRuleMutation{Action: "delete", ID: id}, now)
	if err != nil || len(rules) != 1 || rules[0].ID != keep.ID || !rules[0].ExpiresAt.Equal(*keep.ExpiresAt) {
		t.Fatal("deletion changed another rule")
	}
}

type failingHeaderStore struct {
	memoryAuthStore
	fail bool
}

type managementHeaderPlugin struct{}

func (managementHeaderPlugin) PrepareCodexHeaders(context.Context, pluginapi.CodexHeaderRequest) (pluginapi.CodexHeaderResponse, error) {
	return pluginapi.CodexHeaderResponse{}, nil
}

func (managementHeaderPlugin) ObserveCodexHeaders(context.Context, pluginapi.CodexHeaderObservation) error {
	return nil
}

func (managementHeaderPlugin) CompleteCodexHeaders(context.Context, pluginapi.CodexHeaderCompletion) error {
	return nil
}

func activeManagementHeaderHost() *pluginhost.Host {
	host := pluginhost.New()
	host.RegisterPluginForTest("codex-headers", pluginapi.Plugin{
		Capabilities: pluginapi.Capabilities{CodexHeaderPlugin: managementHeaderPlugin{}},
	})
	return host
}

func (s *failingHeaderStore) Save(ctx context.Context, a *coreauth.Auth) (string, error) {
	if s.fail {
		return "", errors.New("disk unavailable")
	}
	return s.memoryAuthStore.Save(ctx, a)
}
func TestRequestHeaderRulesAPI(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	store := &failingHeaderStore{}
	manager := coreauth.NewManager(store, nil, nil)
	_, err := manager.Register(context.Background(), &coreauth.Auth{ID: "a", FileName: "a.json", Provider: "codex", Metadata: map[string]any{"access_token": "do-not-return", "note": "kept", "proxy_url": "http://proxy.test"}})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	h.SetPluginHost(activeManagementHeaderHost())
	list := func() headerRuleAccount {
		t.Helper()
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/", nil)
		h.GetRequestHeaderRules(c)
		if w.Code != 200 || bytes.Contains(w.Body.Bytes(), []byte("do-not-return")) {
			t.Fatal("list leaked credentials or failed")
		}
		var out struct {
			Accounts []headerRuleAccount `json:"accounts"`
			ReadOnly bool                `json:"read_only"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.Accounts) != 1 {
			t.Fatal("invalid account list")
		}
		if out.ReadOnly == h.headerRulesWritable() {
			t.Fatal("incorrect read-only state")
		}
		return out.Accounts[0]
	}
	mutate := func(input headerRuleMutation, want int) {
		t.Helper()
		data, _ := json.Marshal(input)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(data))
		h.MutateRequestHeaderRules(c)
		if w.Code != want {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
	}
	before := list()
	input := headerRuleMutation{AuthID: "a", Revision: before.Revision, Action: "save", Rule: headerRuleDraft{Name: "Version", Operation: "override", Value: "test", Models: []string{"gpt-5.4"}, DurationMinutes: 10}}
	h.SetPluginHost(nil)
	mutate(input, http.StatusServiceUnavailable)
	if after := list(); after.Revision != before.Revision || len(after.Rules) != 0 {
		t.Fatal("unavailable plugin changed rules")
	}
	h.SetPluginHost(activeManagementHeaderHost())
	mutate(input, 200)
	after := list()
	if len(after.Rules) != 1 || after.Rules[0].ExpiresAt == nil {
		t.Fatal("rule not saved")
	}
	mutate(input, 409)
	auth, _ := manager.GetByID("a")
	if auth.Metadata["access_token"] != "do-not-return" || auth.Metadata["note"] != "kept" || auth.Metadata["proxy_url"] != "http://proxy.test" {
		t.Fatal("unrelated account data changed")
	}
	persisted, _ := store.List(context.Background())
	decoded, err := authheaders.Decode(persisted[0].Metadata[authheaders.MetadataKey])
	if err != nil || !decoded[0].ExpiresAt.Equal(*after.Rules[0].ExpiresAt) {
		t.Fatal("deadline not persisted")
	}
	input.Revision = after.Revision
	input.ID = after.Rules[0].ID
	input.Rule.Name = "Authorization"
	mutate(input, 400)
	input.Rule.Name = "Version"
	input.Rule.Value = "new"
	for _, host := range []*pluginhost.Host{nil, pluginhost.New()} {
		h.SetPluginHost(host)
		for _, action := range []string{"save", "delete", "restart"} {
			blocked := input
			blocked.Action = action
			mutate(blocked, http.StatusServiceUnavailable)
		}
		if current := list(); current.Revision != after.Revision {
			t.Fatal("read-only mutation changed persisted rules")
		}
	}
	h.SetPluginHost(activeManagementHeaderHost())
	store.fail = true
	mutate(input, 500)
}
