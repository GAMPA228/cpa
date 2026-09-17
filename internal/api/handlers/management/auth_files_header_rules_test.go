package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/authheaders"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestAuthFileHeaderRulesRoundTrip(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	manager := coreauth.NewManager(&memoryAuthStore{}, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	const content = `{"type":"codex","note":"keep","request_header_rules":[{"name":"User-Agent","operation":"override","value":"account-agent"}]}`
	if err := h.writeAuthFile(context.Background(), "account.json", []byte(content)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg.AuthDir, "account.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := synthesizer.SynthesizeAuthFile(&synthesizer.SynthesisContext{Config: cfg, AuthDir: cfg.AuthDir, IDGenerator: synthesizer.NewStableIDGenerator()}, path, data)
	if err != nil || len(loaded) != 1 {
		t.Fatalf("reload failed: %v", err)
	}
	rules, err := authheaders.Decode(loaded[0].Metadata[authheaders.MetadataKey])
	if err != nil || len(rules) != 1 || rules[0].Value != "account-agent" {
		t.Fatal("rules lost after reload")
	}
	bad := strings.Replace(content, "User-Agent", "Authorization", 1)
	if err := h.writeAuthFile(context.Background(), "account.json", []byte(bad)); err == nil {
		t.Fatal("accepted protected header")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(data) {
		t.Fatal("invalid upload overwrote existing auth")
	}
	if _, err := synthesizer.SynthesizeAuthFile(&synthesizer.SynthesisContext{Config: cfg}, path, []byte(bad)); err == nil {
		t.Fatal("hot reload accepted protected header")
	}

	patch := func(body string, want int) {
		t.Helper()
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(body))
		h.PatchAuthFileFields(ctx)
		if rec.Code != want {
			t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
		}
	}
	patch(`{"name":"account.json","request_header_rules":[{"name":"Authorization","operation":"delete"}]}`, http.StatusBadRequest)
	current, _ := manager.GetByID("account.json")
	rules, _ = authheaders.Decode(current.Metadata[authheaders.MetadataKey])
	if len(rules) != 1 || rules[0].Name != "User-Agent" {
		t.Fatal("rejected patch changed runtime")
	}
	patch(`{"name":"account.json","request_header_rules":[]}`, http.StatusOK)
	current, _ = manager.GetByID("account.json")
	rules, err = authheaders.Decode(current.Metadata[authheaders.MetadataKey])
	if err != nil || len(rules) != 0 || current.Metadata["note"] != "keep" {
		t.Fatal("clear did not preserve other settings")
	}
}
