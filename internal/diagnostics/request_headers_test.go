package diagnostics

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestRequestHeaderSnapshotRedaction(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	credentials := []string{"Authorization", "aUtHoRiZaTiOn", "Cookie", "Cookie2", "Proxy-Authorization", "Proxy_Authentication", "X-Api-Key", "api_key", "X-Goog-Api-Key", "X-Provider-API.Key", "X-Auth-Token", "X-Amz-Security-Token", "X-Client-Secret", "X-Password", "X-Credentials", "X-Auth"}
	for _, key := range credentials {
		req.Header[key] = []string{"secret-one", "secret-two"}
	}
	req.Header["X-Multiple"] = []string{"first", "second"}
	req.Header["Host"] = []string{"not-sent.example"}
	req.Host = "override.example:8443"
	before := req.Header.Clone()
	snapshot := SnapshotRequestHeaders(req)
	if snapshot.Truncated || snapshot.Headers.Get("Host") != req.Host {
		t.Fatalf("invalid snapshot: %+v", snapshot)
	}
	for _, key := range credentials {
		if !reflect.DeepEqual(snapshot.Headers[http.CanonicalHeaderKey(key)], []string{"[REDACTED]"}) {
			t.Errorf("credential header %q was not redacted", key)
		}
	}
	if !reflect.DeepEqual(req.Header, before) {
		t.Fatal("request headers were mutated")
	}
	req.Header["X-Multiple"][0] = "changed"
	if !reflect.DeepEqual(snapshot.Headers.Values("X-Multiple"), []string{"first", "second"}) {
		t.Fatal("snapshot aliases source values")
	}
	req.Host = ""
	if got := SnapshotRequestHeaders(req).Headers.Get("Host"); got != "example.com" {
		t.Fatalf("URL host fallback = %q", got)
	}
}

func TestHTTPRequestHeadersPersistFinalApplicationRequest(t *testing.T) {
	m, path := testManager(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer actual-secret" || r.Host != "virtual.example" {
			t.Error("capture changed the upstream request")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	ctx := m.Context(context.Background())
	Begin(ctx, Metadata{URL: server.URL, Method: http.MethodGet}, nil, false)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "virtual.example"
	req.Header.Set("Authorization", "Bearer actual-secret")
	req.Header["X-Final"] = []string{"one", "two"}
	client := server.Client()
	WrapHTTP(ctx, client)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	m.Close()
	if err := m.Open(path); err != nil {
		t.Fatal(err)
	}
	items := captures(t, m, ID(ctx))
	if len(items) != 1 {
		t.Fatalf("capture count = %d", len(items))
	}
	headers := items[0].RequestHeaders
	if headers.Get("Host") != req.Host || headers.Get("Authorization") != "[REDACTED]" || !reflect.DeepEqual(headers.Values("X-Final"), []string{"one", "two"}) {
		t.Fatalf("unexpected persisted headers: %v", headers)
	}
	data, err := json.Marshal(items[0])
	if err != nil || !strings.Contains(string(data), `"request_headers"`) || strings.Contains(string(data), "actual-secret") {
		t.Fatalf("unsafe or missing JSON headers: %s, %v", data, err)
	}
}

func TestRequestHeadersMemoryLimitsAndRelease(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
	req.Header["X-Empty"] = nil
	req.Header["X-Values"] = []string{"one", "two"}
	snapshot := SnapshotRequestHeaders(req)
	size := requestHeaderSize(snapshot.Headers)
	for _, available := range []int{size, size - 1} {
		m := NewManager()
		m.memory = memoryLimit - available
		a := &Attempt{manager: m}
		a.RequestHeaders(snapshot)
		if available < size {
			if !a.data.Truncated || a.data.Reason != "request_header_size_limit" || a.bytes != 0 || a.data.RequestHeaders != nil {
				t.Fatal("memory limit did not reject headers")
			}
			continue
		}
		if m.memory != memoryLimit || a.bytes != size {
			t.Fatal("header memory not accounted")
		}
		a.RequestHeaders(snapshot)
		if a.bytes != size {
			t.Fatal("headers counted twice")
		}
		snapshot.Headers.Set("X-Values", "changed")
		if a.data.RequestHeaders.Get("X-Values") != "one" {
			t.Fatal("capture aliases snapshot")
		}
		a.Finish("", false) // A nil queue drops immediately and releases memory.
		if m.memory != memoryLimit-available || a.data.RequestHeaders != nil {
			t.Fatal("dropped capture retained headers or memory")
		}
		a.RequestHeaders(snapshot)
		if a.data.RequestHeaders != nil {
			t.Fatal("finished attempt accepted headers")
		}
		snapshot = SnapshotRequestHeaders(req)
	}
	req.Header.Set("X-Large", strings.Repeat("x", 256<<10))
	oversized := SnapshotRequestHeaders(req)
	if !oversized.Truncated || oversized.Headers != nil {
		t.Fatal("oversized snapshot retained")
	}
	m := NewManager()
	a := &Attempt{manager: m}
	a.RequestHeaders(oversized)
	if !a.data.Truncated || m.memory != 0 {
		t.Fatal("oversized snapshot not marked or accounted correctly")
	}
}

func TestRequestHeadersActiveCaptureClone(t *testing.T) {
	m, path := testManager(t)
	ctx := m.Context(context.Background())
	a := Begin(ctx, Metadata{}, nil, false)
	req, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
	req.Header["X-Values"] = []string{"first", "second"}
	a.RequestHeaders(SnapshotRequestHeaders(req))
	items := captures(t, m, ID(ctx))
	items[0].RequestHeaders["X-Values"][0] = "mutated"
	items[0].RequestHeaders.Set("Authorization", "injected-secret")
	got := captures(t, m, ID(ctx))[0].RequestHeaders
	if got.Get("X-Values") != "first" || got.Get("Authorization") != "" {
		t.Fatal("active capture aliases returned request headers")
	}
	a.Finish("", false)
	m.Close()
	if m.memory != 0 || a.data.RequestHeaders != nil {
		t.Fatal("persisted capture retained header memory")
	}
	if err := m.Open(path); err != nil {
		t.Fatal(err)
	}
	got = captures(t, m, ID(ctx))[0].RequestHeaders
	if got.Get("X-Values") != "first" || got.Get("Authorization") != "" {
		t.Fatal("persisted capture was mutated through an active snapshot")
	}
}
