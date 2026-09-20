package helps

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/authheaders"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/turnstate"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestCodexTurnStateObservationEligibilityAndMetadata(t *testing.T) {
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
	data := make([]byte, 233)
	data[0] = 0x80
	binary.BigEndian.PutUint64(data[1:9], uint64(time.Now().Unix()))
	token := base64.URLEncoding.EncodeToString(data)
	if len(token) != 312 {
		t.Fatal("fixture must be 312 characters")
	}
	auth := &cliproxyauth.Auth{ID: "a", Provider: "codex", Metadata: map[string]any{"access_token": "test"}}
	r := NewUsageReporter(context.Background(), "codex", "model", auth)
	length := func() *int {
		return r.buildRecordForModel("model", usage.Detail{}, false, usage.Failure{}).TurnStateLength
	}
	response := &http.Response{StatusCode: 200, Header: http.Header{turnstate.Header: []string{token}}}
	ObserveCodexTurnState(r, auth, "model", response)
	if length() == nil || *length() != 312 {
		t.Fatal("threshold incorrectly filtered length statistics")
	}
	m.Close()
	if err := m.Open(path); err != nil {
		t.Fatal(err)
	}
	if len(m.Rules("a")) != 0 {
		t.Fatal("oversized rule accepted")
	}
	if err := m.Configure(turnstate.Settings{Enabled: true, MaxChars: 312}); err != nil {
		t.Fatal(err)
	}
	response.StatusCode = 503
	ObserveCodexTurnState(r, auth, "failed-model", response)
	response.StatusCode = 200
	apiKeyAuth := &cliproxyauth.Auth{ID: "api", Provider: "codex", Attributes: map[string]string{"api_key": "test"}}
	ObserveCodexTurnState(r, apiKeyAuth, "model", response)
	ObserveCodexTurnStateEvent(r, auth, "model", []byte(fmt.Sprintf(`{"type":"codex.response.metadata","headers":{"x-codex-turn-state":%q}}`, token)))
	m.Close()
	if err := m.Open(path); err != nil {
		t.Fatal(err)
	}
	if got := m.Rules("a"); len(got) != 1 || got[0].Model != "model" {
		t.Fatal("fresh metadata event not observed or failed response accepted")
	}
	if len(m.Rules("api")) != 0 {
		t.Fatal("API key account auto-enrolled")
	}
	if length() == nil || *length() != 312 {
		t.Fatal("metadata length missing")
	}
	r.SetTurnStateLength(nil)
	ObserveCodexTurnState(r, auth, "model", nil)
	ObserveCodexTurnStateEvent(r, auth, "model", []byte(`{"type":"response.output_text.delta","headers":{"X-Codex-Turn-State":"not-a-header"}}`))
	if length() != nil {
		t.Fatal("reused handshake or ordinary data falsely observed")
	}
	ObserveCodexTurnStateEvent(r, auth, "model", []byte(`{"type":"codex.response.metadata","headers":{"X-Codex-Turn-State":42}}`))
	if length() != nil {
		t.Fatal("nonstring metadata accepted")
	}
	response.Header = http.Header{turnstate.Header: []string{"one", "two"}}
	ObserveCodexTurnState(r, auth, "model", response)
	if length() != nil {
		t.Fatal("ambiguous duplicate headers accepted")
	}
}

func TestCodexTurnStateRefreshRemovesAllCasingsAndChangesConnectionKey(t *testing.T) {
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
	binary.BigEndian.PutUint64(data[1:9], uint64(time.Now().Add(-56*time.Minute).Unix()))
	token := base64.URLEncoding.EncodeToString(data)
	m.Observe("a", "model", token)
	m.Close()
	if err := m.Open(path); err != nil {
		t.Fatal(err)
	}
	auth := &cliproxyauth.Auth{ID: "a", Provider: "codex", Metadata: map[string]any{
		"access_token":          "test",
		authheaders.MetadataKey: []authheaders.Rule{{Name: "User-Agent", Operation: "override", Value: "custom-agent"}},
	}}
	headers := http.Header{turnstate.Header: {"client-old"}, "x-codex-turn-state": {"second-old"}}
	firstKey, first := PrepareCodexAccountHeaders(headers, auth, "model")
	if first == nil || len(headers) != 1 || headers.Get("User-Agent") != "custom-agent" {
		t.Fatal("refresh did not remove all state casings or changed unrelated account rules")
	}
	concurrentHeaders := http.Header{"x-codex-turn-state": {"client-old"}}
	normalKey, concurrent := PrepareCodexAccountHeaders(concurrentHeaders, auth, "model")
	if concurrent != nil || normalKey == firstKey || concurrentHeaders.Get(turnstate.Header) != token || len(concurrentHeaders) != 2 {
		t.Fatal("concurrent request did not retain one canonical, valid automatic value")
	}
	first.Finish(false)
	secondKey, second := PrepareCodexAccountHeaders(http.Header{}, auth, "model")
	if second == nil || secondKey == firstKey || secondKey == normalKey {
		t.Fatal("next refresh can reuse a stale headerless WebSocket handshake")
	}
	second.Finish(true)
	if err := m.Configure(turnstate.Settings{Enabled: true, MaxChars: 292, AccountScope: "selected"}); err != nil {
		t.Fatal(err)
	}
	headers = http.Header{turnstate.Header: {"client-original"}}
	excludedKey, excluded := PrepareCodexAccountHeaders(headers, auth, "model")
	if excluded != nil || headers.Get(turnstate.Header) != "client-original" || excludedKey == normalKey || headers.Get("User-Agent") != "custom-agent" {
		t.Fatal("excluded account mutated client state, retained connection key, or lost manual rule")
	}
	if err := m.Configure(turnstate.Settings{Enabled: false, MaxChars: 292}); err != nil {
		t.Fatal(err)
	}
	headers = http.Header{turnstate.Header: {"client-original"}}
	_, disabled := PrepareCodexAccountHeaders(headers, auth, "model")
	if disabled != nil || headers.Get(turnstate.Header) != "client-original" {
		t.Fatal("disabled automation failed to restore normal client header handling")
	}
}
