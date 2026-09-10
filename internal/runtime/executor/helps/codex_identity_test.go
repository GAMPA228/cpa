package helps

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func identityTestConfig(t *testing.T) *config.Config {
	t.Helper()
	enabled := true
	return &config.Config{AuthDir: t.TempDir(), Codex: config.CodexConfig{SingleDevice: &enabled}}
}

func identityTestContext(principal, ua string) context.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("User-Agent", ua)
	c.Request.Header.Set("Session-Id", "same-session")
	c.Request.Header.Set("Thread-Id", "child-session")
	c.Request.Header.Set("X-Codex-Window-Id", "window-1")
	c.Request.Header.Set("X-Client-Request-Id", "request-1")
	c.Request.Header.Set("session_id", "legacy-session")
	c.Request.Header.Set("X-Codex-Turn-Metadata", `{"parent_thread_id":"same-session","turn_id":"turn-1"}`)
	c.Set("userApiKey", principal)
	return context.WithValue(context.Background(), "gin", c)
}

func TestCodexSingleDeviceIsolationAndPersistence(t *testing.T) {
	cfg := identityTestConfig(t)
	auth := &cliproxyauth.Auth{ID: "file-a", Provider: "codex", Metadata: map[string]any{"account_id": "account-a", "email": "a@example.com"}}
	body := []byte(`{"prompt_cache_key":"cache-1","previous_response_id":"resp_123","input":"same-session","client_metadata":{"x-codex-installation-id":"device-a"}}`)
	prepare := func(principal string) ([]byte, *CodexIdentity, http.Header) {
		t.Helper()
		out, state, err := PrepareCodexIdentity(identityTestContext(principal, "codex-tui/0.153.3"), cfg, auth, nil, body)
		if err != nil || state == nil {
			t.Fatalf("prepare: %v, state %v", err, state)
		}
		headers := http.Header{"Session_id": {"proxy-generated"}}
		state.ApplyHeaders(headers)
		return out, state, headers
	}
	outA, stateA, headersA := prepare("user-a")
	outB, stateB, headersB := prepare("user-b")
	if stateA.device != stateB.device {
		t.Fatal("same account must use one device")
	}
	if headersA.Get("Session-Id") == headersB.Get("Session-Id") {
		t.Fatal("different users share a session")
	}
	if gjson.GetBytes(outA, "prompt_cache_key").String() == gjson.GetBytes(outB, "prompt_cache_key").String() {
		t.Fatal("different users share a cache key")
	}
	if headersA.Get("Thread-Id") == headersA.Get("Session-Id") {
		t.Fatal("child thread collapsed into parent")
	}
	if gjson.Get(headersA.Get("X-Codex-Turn-Metadata"), "parent_thread_id").String() != headersA.Get("Session-Id") {
		t.Fatal("parent relationship lost")
	}
	if headersA.Get("X-Client-Request-Id") == headersA.Get("Session-Id") {
		t.Fatal("request collapsed into session")
	}
	if identityHeader(headersA, "session_id") != "" {
		t.Fatal("modern Codex underscore session header retained")
	}
	if gjson.GetBytes(outA, "previous_response_id").String() != "resp_123" || gjson.GetBytes(outA, "input").String() != "same-session" {
		t.Fatal("continuation or input changed")
	}
	if !bytes.Contains(body, []byte("device-a")) {
		t.Fatal("original body mutated")
	}
	keyPath := filepath.Join(cfg.AuthDir, ".codex-identity-key")
	keyBefore, err := os.ReadFile(keyPath)
	if err != nil || len(keyBefore) != 32 {
		t.Fatalf("persisted key: %v", err)
	}
	codexIdentityKeys.Lock()
	delete(codexIdentityKeys.values, keyPath)
	codexIdentityKeys.Unlock()
	auth.ID = "renamed-file"
	auth.Metadata["access_token"] = "rotated-token"
	outAgain, stateAgain, headersAgain := prepare("user-a")
	if stateAgain.device != stateA.device || !bytes.Equal(outAgain, outA) || headersAgain.Get("Session-Id") != headersA.Get("Session-Id") {
		t.Fatal("restart or token rotation changed identity")
	}
	auth.Metadata["account_id"] = "account-b"
	_, stateOther, headersOther := prepare("user-a")
	if stateOther.device == stateA.device || headersOther.Get("Session-Id") == headersA.Get("Session-Id") {
		t.Fatal("account switch reused identity")
	}
}

func TestCodexSingleDeviceNonCodexAndDisabled(t *testing.T) {
	cfg := identityTestConfig(t)
	auth := &cliproxyauth.Auth{ID: "auth", Provider: "codex", Metadata: map[string]any{"email": "a@example.com"}}
	body := []byte(`{"prompt_cache_key":"cache","client_metadata":{"x-codex-installation-id":"untouched"}}`)
	out, state, err := PrepareCodexIdentity(identityTestContext("user", "curl/8.0"), cfg, auth, nil, body)
	if err != nil || state == nil {
		t.Fatalf("prepare: %v", err)
	}
	headers := http.Header{"X-Codex-Window-Id": {"generated"}, "Thread-Id": {"generated"}}
	state.ApplyHeaders(headers)
	if gjson.GetBytes(out, "prompt_cache_key").String() == "cache" {
		t.Fatal("cache not mapped")
	}
	if gjson.GetBytes(out, "client_metadata.x-codex-installation-id").String() != "untouched" {
		t.Fatal("non-Codex device changed")
	}
	if headers.Get("X-Codex-Installation-Id") != "" || headers.Get("Thread-Id") != "child-session" {
		t.Fatal("non-Codex identity synthesized")
	}
	*cfg.Codex.SingleDevice = false
	out, state, err = PrepareCodexIdentity(context.Background(), cfg, auth, nil, body)
	if err != nil || state != nil || !bytes.Equal(out, body) {
		t.Fatal("disabled mode modified request")
	}
	*cfg.Codex.SingleDevice = true
	auth.Attributes = map[string]string{"auth_kind": "apikey", "api_key": "test"}
	out, state, err = PrepareCodexIdentity(context.Background(), cfg, auth, nil, body)
	if err != nil || state != nil || !bytes.Equal(out, body) {
		t.Fatal("API-key auth modified")
	}
}

func TestCodexSingleDeviceResponseAndKeyFailure(t *testing.T) {
	state := &CodexIdentity{reverse: map[string]string{"mapped-cache": "original-cache"}}
	payload := []byte("data: {\"response\":{\"prompt_cache_key\":\"mapped-cache\",\"id\":\"mapped-cache\"},\"delta\":\"mapped-cache\"}\n\ndata: [DONE]\n")
	out := state.RestoreResponse(payload)
	if !bytes.Contains(out, []byte(`"prompt_cache_key":"original-cache"`)) || !bytes.Contains(out, []byte(`"delta":"mapped-cache"`)) || !bytes.Contains(out, []byte(`"id":"mapped-cache"`)) {
		t.Fatalf("response corrupted: %s", out)
	}
	cfg := identityTestConfig(t)
	if err := os.WriteFile(filepath.Join(cfg.AuthDir, ".codex-identity-key"), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := PrepareCodexIdentity(context.Background(), cfg, &cliproxyauth.Auth{ID: "a", Provider: "codex", Metadata: map[string]any{"email": "a@example.com"}}, nil, []byte(`{}`))
	if err == nil {
		t.Fatal("corrupt key silently replaced")
	}
}
