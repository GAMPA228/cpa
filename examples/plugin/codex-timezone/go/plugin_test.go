package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

func environmentBody(t *testing.T) []byte {
	t.Helper()
	text := "<environment_context>\n<current_date>2001-01-01</current_date>\n<timezone> Asia/Shanghai </timezone>\n<cwd>/app</cwd>\n</environment_context>"
	body, err := json.Marshal(map[string]any{"input": []any{map[string]any{"role": "user", "content": []any{map[string]string{"type": "input_text", "text": text}}}}, "tools": []any{map[string]any{"type": "web_search", "user_location": map[string]string{"timezone": "Asia/Shanghai", "city": "Shanghai"}}}, "reasoning": map[string]string{"effort": "high"}, "service_tier": "priority", "model": "gpt-6"})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestRewritePreservesOtherFields(t *testing.T) {
	body := environmentBody(t)
	got := rewriteBody(body, "America/New_York")
	text := gjson.GetBytes(got, "input.0.content.0.text").String()
	if !strings.Contains(text, "<timezone> America/New_York </timezone>") || !strings.Contains(text, "2001-01-01") {
		t.Fatal(text)
	}
	if gjson.GetBytes(got, "tools.0.user_location.timezone").String() != "America/New_York" {
		t.Fatal(string(got))
	}
	for _, path := range []string{"model", "reasoning", "service_tier", "tools.0.user_location.city"} {
		if gjson.GetBytes(body, path).Raw != gjson.GetBytes(got, path).Raw {
			t.Fatalf("changed %s", path)
		}
	}
	if !bytes.Equal(got, rewriteBody(got, "America/New_York")) {
		t.Fatal("not idempotent")
	}
}

func TestRewriteSkipsUnrelatedAndMalformedContent(t *testing.T) {
	for _, text := range []string{
		"Please use Asia/Shanghai", "prefix <environment_context><timezone>Asia/Shanghai</timezone></environment_context>",
		"<environment_context><cwd>/app</cwd></environment_context>",
		"<environment_context><timezone></timezone></environment_context>",
		"<environment_context><timezone>UTC</timezone><timezone>Asia/Shanghai</timezone></environment_context>",
		"<environment_context><timezone><nested/></timezone></environment_context>",
		"<environment_context><metadata><timezone>Asia/Shanghai</timezone></metadata></environment_context>",
		"<environment_context><timezone>UTC</timezone><broken></environment_context>",
	} {
		if got := rewriteEnvironment(text, "Asia/Tokyo"); got != text {
			t.Fatalf("unexpected rewrite: %s", got)
		}
	}
	body := []byte(`{"input":[{"role":"assistant","content":"<environment_context><timezone>UTC</timezone></environment_context>"}],"tools":[{"type":"web_search"}]}`)
	if !bytes.Equal(body, rewriteBody(body, "Asia/Tokyo")) {
		t.Fatal("modified assistant or added timezone")
	}
	if !bytes.Equal(body, rewriteBody(body, "Invalid/Zone")) {
		t.Fatal("accepted invalid timezone")
	}
}

func TestInterceptIsolationAndDisable(t *testing.T) {
	p := newPlugin(nil)
	p.accounts = map[string]accountView{"a": {ID: "a"}, "b": {ID: "b"}, "c": {ID: "c"}}
	p.settings = settings{Enabled: true, Default: rule{Mode: "manual", Timezone: "Asia/Tokyo"}, Accounts: map[string]rule{"b": {Mode: "manual", Timezone: "America/New_York"}, "c": {Mode: "keep"}}}
	body := environmentBody(t)
	for _, tc := range []struct{ id, zone string }{{"a", "Asia/Tokyo"}, {"b", "America/New_York"}, {"c", ""}, {"other-provider", ""}} {
		out := p.intercept(pluginapi.RequestInterceptRequest{ToFormat: "codex", Body: body, Metadata: map[string]any{"selected_auth_id": tc.id}})
		if tc.zone == "" {
			if len(out.Body) != 0 {
				t.Fatal("keep rule rewrote request")
			}
		} else if !strings.Contains(string(out.Body), tc.zone) {
			t.Fatal("wrong account timezone")
		}
		if len(out.Headers) != 0 || out.Terminate {
			t.Fatal("modified headers or execution")
		}
	}
	p.settings.Enabled = false
	if len(p.intercept(pluginapi.RequestInterceptRequest{ToFormat: "codex", Body: body, Metadata: map[string]any{"selected_auth_id": "a"}}).Body) != 0 {
		t.Fatal("disabled plugin rewrote request")
	}
	p.settings.Enabled = true
	if len(p.intercept(pluginapi.RequestInterceptRequest{ToFormat: "claude", Body: body, Metadata: map[string]any{"selected_auth_id": "a"}}).Body) != 0 {
		t.Fatal("non-Codex request rewritten")
	}
}

func fixtureHost(t *testing.T, proxies map[string]string) hostCall {
	t.Helper()
	return func(method string, req, out any) error {
		var value any
		switch method {
		case "host.auth.list":
			files := []pluginapi.HostAuthFileEntry{}
			for id := range proxies {
				files = append(files, pluginapi.HostAuthFileEntry{ID: id, AuthIndex: id, Provider: "codex", Name: id})
			}
			value = map[string]any{"files": files}
		case "host.auth.get":
			id := req.(map[string]string)["auth_index"]
			value = map[string]any{"json": map[string]string{"proxy_url": proxies[id], "access_token": "must-not-leak"}}
		default:
			return errors.New("unexpected callback")
		}
		raw, _ := json.Marshal(value)
		return json.Unmarshal(raw, out)
	}
}

func TestAutomaticRoutesCacheAndProxyChange(t *testing.T) {
	proxies := map[string]string{"a": "socks5://user:password@localhost:1234", "b": "socks5://user:password@localhost:1234", "c": "", "d": "direct"}
	p := newPlugin(fixtureHost(t, proxies))
	p.settings.Enabled = true
	p.settings.Default = rule{Mode: "auto"}
	now := time.Date(2026, 9, 28, 1, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return now }
	calls := map[string]int{}
	p.probe = func(_ context.Context, proxy string) (observation, error) {
		calls[proxy]++
		return observation{IP: "203.0.113.1", Timezone: "Asia/Tokyo"}, nil
	}
	p.refresh(context.Background(), false)
	if len(calls) != 3 || calls[proxies["a"]] != 1 || p.accounts["d"].Route != "direct" || p.accounts["c"].Route != "global" {
		t.Fatalf("route mismatch: %+v", calls)
	}
	p.refresh(context.Background(), false)
	if calls[proxies["a"]] != 1 {
		t.Fatal("cache missed")
	}
	proxies["a"] = "http://localhost:9876"
	p.refresh(context.Background(), false)
	if calls[proxies["a"]] != 1 {
		t.Fatal("new proxy not queried")
	}
	snapshot, _ := json.Marshal(p.snapshot())
	for _, secret := range []string{"password", "must-not-leak", "socks5://", "localhost:9876"} {
		if bytes.Contains(snapshot, []byte(secret)) {
			t.Fatalf("leaked %s", secret)
		}
	}
	now = now.Add(6 * time.Hour)
	p.refresh(context.Background(), false)
	if calls[proxies["a"]] != 2 {
		t.Fatal("expired cache not refreshed")
	}
}

func TestFailurePreservesLastResultAndBacksOff(t *testing.T) {
	p := newPlugin(fixtureHost(t, map[string]string{"a": "direct"}))
	p.settings.Enabled = true
	p.settings.Default = rule{Mode: "auto"}
	now := time.Now()
	p.now = func() time.Time { return now }
	calls := 0
	p.probe = func(context.Context, string) (observation, error) {
		calls++
		if calls == 1 {
			return observation{IP: "203.0.113.1", Timezone: "Asia/Tokyo"}, nil
		}
		return observation{}, errors.New("unavailable")
	}
	p.refresh(context.Background(), false)
	now = now.Add(6 * time.Hour)
	p.refresh(context.Background(), false)
	p.refresh(context.Background(), false)
	o := p.cache[routeKey("direct")]
	if calls != 2 || o.Error == "" || o.Timezone != "Asia/Tokyo" {
		t.Fatal("failure did not preserve/cache previous result")
	}
	p.refresh(context.Background(), true)
	if calls != 3 {
		t.Fatal("manual refresh was suppressed")
	}
}

func TestPersistenceAndProtectedResources(t *testing.T) {
	p := newPlugin(nil)
	p.path = filepath.Join(t.TempDir(), "nested", "settings.json")
	s := settings{Enabled: true, Default: rule{Mode: "manual", Timezone: "Asia/Tokyo"}, Accounts: map[string]rule{}}
	if err := p.save(s); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p.path)
	if err != nil {
		t.Fatal(err)
	}
	var loaded settings
	if json.Unmarshal(raw, &loaded) != nil || loaded.Default.Timezone != "Asia/Tokyo" {
		t.Fatal("settings not persisted")
	}
	s.Default.Timezone = "Invalid/Zone"
	if p.save(s) == nil {
		t.Fatal("accepted bad timezone")
	}
	if p.settings.Default.Timezone != "Asia/Tokyo" {
		t.Fatal("failed save changed memory")
	}
	page := p.management(pluginapi.ManagementRequest{Method: "GET", Path: "/v0/resource/plugins/codex-timezone/panel"})
	if page.StatusCode != 200 || bytes.Contains(page.Body, []byte("selected_auth_id")) {
		t.Fatal("invalid public resource")
	}
	if p.management(pluginapi.ManagementRequest{Method: "GET", Path: "/v0/resource/plugins/codex-timezone/settings"}).StatusCode != 404 {
		t.Fatal("public data route exposed")
	}
	if p.management(pluginapi.ManagementRequest{Method: "PUT", Path: apiPath, Body: []byte(`{"enabled":true,"default":{"mode":"keep"},"unexpected":true}`)}).StatusCode != 400 {
		t.Fatal("unknown fields accepted")
	}
}

func TestParseLocation(t *testing.T) {
	for _, body := range []string{`{}`, `{"success":true,"ip":"bad","timezone":{"id":"Asia/Tokyo"}}`, `{"success":true,"ip":"203.0.113.1","timezone":{"id":"Local"}}`} {
		if _, err := parseLocation([]byte(body)); err == nil {
			t.Fatal("accepted invalid response")
		}
	}
	if _, err := parseLocation([]byte(`{"success":true,"ip":"203.0.113.1","timezone":{"id":"Asia/Tokyo"}}`)); err != nil {
		t.Fatal(err)
	}
}
