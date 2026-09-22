package main

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/authheaders"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/turnstate"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func encode(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func pluginWithDB(t *testing.T) *headerPlugin {
	t.Helper()
	p := newHeaderPlugin()
	path := filepath.Join(t.TempDir(), "existing.turn-state.sqlite3")
	if err := p.configure(encode(t, lifecycleRequest{ConfigYAML: []byte("data_path: " + filepath.ToSlash(path))})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.close)
	return p
}

func setSettings(t *testing.T, p *headerPlugin, settings turnstate.Settings) {
	t.Helper()
	if err := p.manager.Configure(settings); err != nil {
		t.Fatal(err)
	}
}

func prepare(t *testing.T, p *headerPlugin, req pluginapi.CodexHeaderRequest) pluginapi.CodexHeaderResponse {
	t.Helper()
	resp, err := p.prepare(encode(t, req))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func token(issued time.Time) string {
	data := make([]byte, 73)
	data[0] = 0x80
	binary.BigEndian.PutUint64(data[1:9], uint64(issued.Unix()))
	return base64.URLEncoding.EncodeToString(data)
}

func awaitToken(t *testing.T, p *headerPlugin, account, model string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := p.manager.Lookup(account, model); ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("token did not persist")
		}
		runtime.Gosched()
	}
}

func TestRegistrationAndDefaultDisabledFallback(t *testing.T) {
	base := filepath.Join(t.TempDir(), "usagecompat.sqlite3")
	t.Setenv("USAGECOMPAT_SQLITE_PATH", base)
	p := newHeaderPlugin()
	t.Cleanup(p.close)
	if registration().SchemaVersion != 7 || registration().SchemaVersion != pluginabi.SchemaVersion || !registration().Capabilities.CodexHeaderPlugin {
		t.Fatal("incorrect registration")
	}
	metadata := registration().Metadata
	if metadata.Name == "" || metadata.Version == "" || metadata.Author == "" || metadata.GitHubRepository == "" {
		t.Fatal("registration must supply all metadata required by the host")
	}
	encoded, err := handleMethod(p, pluginabi.MethodPluginRegister, encode(t, lifecycleRequest{}))
	if err != nil || !strings.Contains(string(encoded), `"codex_header_plugin":true`) {
		t.Fatalf("invalid ABI registration: %s %v", encoded, err)
	}
	if err := p.configure(encode(t, lifecycleRequest{})); err != nil {
		t.Fatal(err)
	}
	if p.path != base+".turn-state.sqlite3" {
		t.Fatalf("legacy storage path = %q", p.path)
	}
	req := pluginapi.CodexHeaderRequest{AuthID: "a", Model: "m", URL: "https://chatgpt.com/", Headers: http.Header{"X-Test": {"old"}, "Authorization": {"Bearer secret"}, "Host": {"chatgpt.com"}}}
	req.Rules = encode(t, []authheaders.Rule{{Name: "X-Test", Operation: "override", Value: "new"}})
	resp := prepare(t, p, req)
	if resp.Headers.Get("X-Test") != "new" || resp.ReservationID != "" {
		t.Fatalf("manual fallback failed: %+v", resp)
	}
	if resp.Headers.Get("Authorization") != "" || resp.Headers.Get("Host") != "" || hasHeader(resp.Headers, "Authorization") || hasHeader(resp.Headers, "Host") {
		t.Fatalf("unchanged protected headers must not be returned: %+v", resp)
	}
	if err := p.observe(encode(t, pluginapi.CodexHeaderObservation{AuthID: "a", Model: "m", URL: req.URL, Headers: http.Header{"Set-Cookie": {"sid=abc; Secure; Path=/"}}})); err != nil {
		t.Fatal(err)
	}
	if got := prepare(t, p, pluginapi.CodexHeaderRequest{AuthID: "a", OAuth: true, Model: "m", URL: req.URL}).Headers.Get("Cookie"); got != "" {
		t.Fatal("non-OAuth observation saved cookie")
	}
	req.Rules = []byte(`{"invalid":"secret-rule-value"}`)
	if got := prepare(t, p, req); got.Headers.Get("X-Test") != "" {
		t.Fatalf("invalid stored rules did not fall back: %+v", got)
	}
}

func TestLegacyDefaultWithoutEnv(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("USAGECOMPAT_SQLITE_PATH", "")
	p := newHeaderPlugin()
	t.Cleanup(p.close)
	if err := p.configure(encode(t, lifecycleRequest{})); err != nil {
		t.Fatal(err)
	}
	if p.path != filepath.Join(dir, "usagecompat.sqlite3.turn-state.sqlite3") {
		t.Fatalf("unexpected legacy default path: %q", p.path)
	}
}

func TestQuiesceStopsHeadersAndClearsCookies(t *testing.T) {
	p := pluginWithDB(t)
	path := p.path
	settings := turnstate.Settings{Enabled: true, MaxChars: 312, LifetimeSeconds: 240}
	setSettings(t, p, settings)
	req := pluginapi.CodexHeaderRequest{AuthID: "a", OAuth: true, Model: "m", URL: "https://example.com/responses"}
	req.Rules = encode(t, []authheaders.Rule{{Name: "X-Test", Operation: "override", Value: "applied"}})
	observation := pluginapi.CodexHeaderObservation{AuthID: "a", OAuth: true, Model: "m", URL: req.URL,
		StatusCode: 200, Headers: http.Header{"Set-Cookie": {"sid=private; Secure; Path=/"}}}
	if err := p.observe(encode(t, observation)); err != nil {
		t.Fatal(err)
	}
	if result := prepare(t, p, req); result.Headers.Get("Cookie") == "" || result.Headers.Get("X-Test") != "applied" {
		t.Fatal("enabled plugin did not apply headers")
	}
	if _, err := handleMethod(p, pluginabi.MethodPluginQuiesce, nil); err != nil {
		t.Fatal(err)
	}
	if result := prepare(t, p, req); len(result.Headers) != 0 || len(result.ClearHeaders) != 0 || result.ReservationID != "" {
		t.Fatal("quiesced plugin still modified headers")
	}
	if err := p.observe(encode(t, observation)); err != nil {
		t.Fatal(err)
	}
	if len(p.jars) != 0 || len(p.reservations) != 0 || p.manager != nil {
		t.Fatal("quiesce retained runtime state")
	}
	if err := p.configure(encode(t, lifecycleRequest{ConfigYAML: []byte("data_path: " + filepath.ToSlash(path))})); err != nil {
		t.Fatal(err)
	}
	if !p.manager.Status().Enabled || p.manager.Status().MaxChars != 312 {
		t.Fatal("settings were not restored")
	}
	if result := prepare(t, p, req); result.Headers.Get("Cookie") != "" || result.Headers.Get("X-Test") != "applied" {
		t.Fatal("re-enabled plugin reused old cookies or lost manual rules")
	}
}

func TestRulePriorityExpiryDefaultDelete(t *testing.T) {
	p := newHeaderPlugin()
	now := time.Now()
	expires := now.Add(-time.Second)
	req := pluginapi.CodexHeaderRequest{AuthID: "a", Model: "special", Headers: http.Header{"x-existing": {"preserved"}, "User-Agent": {"client"}}}
	req.Rules = encode(t, []authheaders.Rule{
		{Name: "X-Existing", Operation: "default", Value: "fallback"},
		{Name: "X-Target", Operation: "override", Value: "global"},
		{Name: "X-Target", Models: []string{"special"}, Operation: "override", Value: "scoped"},
		{Name: "X-Old", Operation: "override", Value: "expired", DurationMinutes: 10, ExpiresAt: &expires},
		{Name: "User-Agent", Operation: "delete"},
	})
	resp := prepare(t, p, req)
	if resp.Headers.Get("X-Target") != "scoped" || resp.Headers.Get("X-Existing") != "" || resp.Headers.Get("X-Old") != "" {
		t.Fatalf("rule selection: %+v", resp)
	}
	if resp.Headers.Get("User-Agent") != "" {
		t.Fatal("user agent deletion must inhibit Go default")
	}
	if values, exists := resp.Headers["User-Agent"]; !exists || len(values) != 1 || values[0] != "" {
		t.Fatal("user agent deletion must explicitly set an empty header")
	}
	if len(resp.ClearHeaders) == 0 {
		t.Fatal("missing clear for existing user agent")
	}
}

func TestTurnStateAccountIsolationRefreshAndCompletion(t *testing.T) {
	p := pluginWithDB(t)
	setSettings(t, p, turnstate.Settings{Enabled: true, MaxChars: 292, AccountScope: "selected", AuthIDs: []string{"a"}})
	now := time.Now().UTC().Truncate(time.Second)
	value := token(now.Add(-55 * time.Minute))
	if err := p.observe(encode(t, pluginapi.CodexHeaderObservation{AuthID: "a", OAuth: true, Model: "m", StatusCode: http.StatusForbidden, Headers: http.Header{turnstate.Header: {value}}})); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.manager.Lookup("a", "m"); ok {
		t.Fatal("error response must not create a turn state rule")
	}
	if err := p.observe(encode(t, pluginapi.CodexHeaderObservation{AuthID: "a", OAuth: true, Model: "m", StatusCode: http.StatusOK, Headers: http.Header{turnstate.Header: {value}}})); err != nil {
		t.Fatal(err)
	}
	awaitToken(t, p, "a", "m")
	req := pluginapi.CodexHeaderRequest{AuthID: "a", OAuth: true, Model: "m", Headers: http.Header{turnstate.Header: {"client"}}}
	first := prepare(t, p, req)
	if first.ReservationID == "" || len(first.ClearHeaders) == 0 || first.Headers.Get(turnstate.Header) != "" {
		t.Fatalf("expected refresh reservation: %+v", first)
	}
	second := prepare(t, p, req)
	if second.ReservationID != "" || second.Headers.Get(turnstate.Header) != value {
		t.Fatalf("active refresh not retained: %+v", second)
	}
	if err := p.complete(encode(t, pluginapi.CodexHeaderCompletion{ReservationID: first.ReservationID, Attempted: false})); err != nil {
		t.Fatal(err)
	}
	third := prepare(t, p, req)
	if third.ReservationID == "" || third.ReservationID == first.ReservationID {
		t.Fatal("completion did not release reservation")
	}
	other := prepare(t, p, pluginapi.CodexHeaderRequest{AuthID: "b", OAuth: true, Model: "m", Headers: http.Header{turnstate.Header: {"untouched"}}})
	if len(other.ClearHeaders) > 0 || other.Headers.Get(turnstate.Header) != "" {
		t.Fatalf("non-opted-in account changed: %+v", other)
	}
	_ = p.complete(encode(t, pluginapi.CodexHeaderCompletion{ReservationID: third.ReservationID}))
}

func TestCookieAccountIsolationAndDisabledFallback(t *testing.T) {
	p := pluginWithDB(t)
	setSettings(t, p, turnstate.Settings{Enabled: true, MaxChars: 292, AccountScope: "selected", AuthIDs: []string{"a", "b"}})
	u := "https://chatgpt.com/backend-api/codex"
	initial := prepare(t, p, pluginapi.CodexHeaderRequest{AuthID: "a", OAuth: true, Model: "m", URL: u}).Signature
	observe := func(auth, cookie string) {
		t.Helper()
		if err := p.observe(encode(t, pluginapi.CodexHeaderObservation{AuthID: auth, OAuth: true, Model: "m", URL: u, Headers: http.Header{"set-cookie": {cookie + "; Secure; Path=/"}}})); err != nil {
			t.Fatal(err)
		}
	}
	observe("a", "sid=alice")
	observe("b", "sid=bob")
	get := func(auth, target string) string {
		t.Helper()
		return prepare(t, p, pluginapi.CodexHeaderRequest{AuthID: auth, OAuth: true, Model: "m", URL: target}).Headers.Get("Cookie")
	}
	if got := get("a", u); !strings.Contains(got, "sid=alice") || strings.Contains(got, "bob") {
		t.Fatalf("account a: %q", got)
	}
	updated := prepare(t, p, pluginapi.CodexHeaderRequest{AuthID: "a", OAuth: true, Model: "m", URL: u}).Signature
	if initial == updated || strings.Contains(updated, "alice") {
		t.Fatal("cookie change did not safely invalidate WS reuse")
	}
	if got := get("b", u); !strings.Contains(got, "sid=bob") || strings.Contains(got, "alice") {
		t.Fatalf("account b: %q", got)
	}
	if got := get("a", "https://other.example/"); got != "" {
		t.Fatalf("cookie escaped origin: %q", got)
	}
	if got := get("a", "http://chatgpt.com/"); got != "" {
		t.Fatalf("cookie escaped https: %q", got)
	}
	if got := get("c", u); got != "" {
		t.Fatal("cookie escaped account")
	}
	if err := p.observe(encode(t, pluginapi.CodexHeaderObservation{AuthID: "b", OAuth: false, Model: "m", URL: u, Headers: http.Header{"Set-Cookie": {"sid=blocked; Secure; Path=/"}}})); err != nil {
		t.Fatal(err)
	}
	if got := get("b", u); !strings.Contains(got, "sid=bob") || strings.Contains(got, "blocked") {
		t.Fatalf("non-OAuth observation updated jar: %q", got)
	}
	if got := get("a", "wss://chatgpt.com/backend-api/codex"); !strings.Contains(got, "sid=alice") {
		t.Fatalf("wss cookies not normalized: %q", got)
	}
	if err := p.observe(encode(t, pluginapi.CodexHeaderObservation{AuthID: "a", OAuth: true, Model: "m", URL: "wss://chatgpt.com/backend-api/codex", Headers: http.Header{"Set-Cookie": {"socket=ok; Secure; Path=/backend-api"}}})); err != nil {
		t.Fatal(err)
	}
	if got := get("a", "https://chatgpt.com/backend-api/codex"); !strings.Contains(got, "socket=ok") {
		t.Fatalf("wss handshake cookie not normalized: %q", got)
	}
	if got := prepare(t, p, pluginapi.CodexHeaderRequest{AuthID: "a", OAuth: false, Model: "m", URL: u}).Headers.Get("Cookie"); got != "" {
		t.Fatal("non-OAuth request applied automatic cookie")
	}
	settings := p.manager.Status().Settings
	settings.Enabled = false
	setSettings(t, p, settings)
	if got := get("a", u); !strings.Contains(got, "sid=alice") {
		t.Fatal("disabling turn state must not disable account cookies")
	}
}

func TestSettingsRouteAndExistingDatabase(t *testing.T) {
	p := pluginWithDB(t)
	raw := encode(t, pluginapi.ManagementRequest{Method: "PUT", Path: settingsPath, Body: encode(t, map[string]any{"enabled": true, "max_chars": 300, "account_scope": "selected", "auth_ids": []string{"a"}})})
	response, err := p.management(raw)
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("put: %+v %v", response, err)
	}
	response, err = p.management(encode(t, pluginapi.ManagementRequest{Method: "GET", Path: settingsPath}))
	if err != nil || response.StatusCode != 200 || !strings.Contains(string(response.Body), "\"max_chars\":300") {
		t.Fatalf("get: %+v %v", response, err)
	}
	path := p.path
	value := token(time.Now().UTC().Truncate(time.Second))
	if err := p.observe(encode(t, pluginapi.CodexHeaderObservation{AuthID: "a", OAuth: true, Model: "m", StatusCode: http.StatusOK, Headers: http.Header{turnstate.Header: {value}}})); err != nil {
		t.Fatal(err)
	}
	p.close()
	if err := p.configure(encode(t, lifecycleRequest{ConfigYAML: []byte("data_path: " + filepath.ToSlash(path))})); err != nil {
		t.Fatal(err)
	}
	if settings := p.manager.Status().Settings; settings.MaxChars != 300 || settings.AccountScope != "selected" || len(settings.AuthIDs) != 1 {
		t.Fatalf("settings not recovered: %+v", settings)
	}
	if rule, ok := p.manager.Lookup("a", "m"); !ok || rule.Value != value {
		t.Fatal("existing state history not recovered")
	}
	view, err := p.management(encode(t, pluginapi.ManagementRequest{Method: "GET", Path: rulesPath}))
	if err != nil || view.StatusCode != 200 {
		t.Fatalf("rule view: %+v %v", view, err)
	}
	var result struct {
		Rules []automaticRuleView `json:"rules"`
	}
	if err := json.Unmarshal(view.Body, &result); err != nil || len(result.Rules) != 1 {
		t.Fatalf("rule view payload: %s %v", view.Body, err)
	}
	if rule := result.Rules[0]; rule.AuthID != "a" || rule.ID == "" || rule.Model != "m" || rule.Value != value || rule.IssuedAt.IsZero() || rule.ExpiresAt.IsZero() || !rule.Active || !rule.AccountEnabled || rule.DurationMinutes != 60 {
		t.Fatalf("invalid rule view: %+v", rule)
	}
	response, err = p.management(encode(t, pluginapi.ManagementRequest{Method: "PUT", Path: settingsPath, Body: encode(t, map[string]any{"enabled": false, "max_chars": 300})}))
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("disable: %+v %v", response, err)
	}
	if settings := p.manager.Status().Settings; settings.AccountScope != "selected" || len(settings.AuthIDs) != 1 {
		t.Fatalf("PUT did not preserve omitted account scope: %+v", settings)
	}
	view, err = p.management(encode(t, pluginapi.ManagementRequest{Method: "GET", Path: rulesPath}))
	if err != nil || json.Unmarshal(view.Body, &result) != nil || len(result.Rules) != 1 || result.Rules[0].Active || result.Rules[0].AccountEnabled {
		t.Fatalf("disabled rule view: %+v %v", view, err)
	}
	bad, err := p.management(encode(t, pluginapi.ManagementRequest{Method: "PUT", Path: settingsPath, Body: []byte(`{"enabled":true,"max_chars":300,"unknown":"secret"}`)}))
	if err != nil || bad.StatusCode != 400 || strings.Contains(string(bad.Body), "secret") {
		t.Fatal("invalid field leaked")
	}
}
