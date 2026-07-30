package usagecompat

import (
	"context"
	"database/sql"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
)

func TestResolveClientIPIgnoresHeadersFromUntrustedPeer(t *testing.T) {
	stats := &RequestStatistics{}
	ctx := clientAddressContext(internallogging.RequestClientAddress{
		RemoteAddr:    "192.0.2.10:4321",
		XForwardedFor: "203.0.113.9",
		XRealIP:       "203.0.113.8",
	})

	if got := stats.resolveClientIP(ctx); got != "192.0.2.10" {
		t.Fatalf("resolveClientIP() = %q, want direct peer", got)
	}
}

func TestResolveClientIPUsesRightmostUntrustedForwardedAddress(t *testing.T) {
	stats := &RequestStatistics{}
	if err := stats.SetTrustedProxies([]string{"10.0.0.0/8", "192.168.0.0/16"}); err != nil {
		t.Fatalf("SetTrustedProxies() error = %v", err)
	}
	ctx := clientAddressContext(internallogging.RequestClientAddress{
		RemoteAddr:    "10.1.2.3:443",
		XForwardedFor: "198.51.100.7, 203.0.113.9, 192.168.1.5",
	})

	if got := stats.resolveClientIP(ctx); got != "203.0.113.9" {
		t.Fatalf("resolveClientIP() = %q, want rightmost untrusted address", got)
	}
}

func TestResolveClientIPFallsBackToRealIPForTrustedPeer(t *testing.T) {
	stats := &RequestStatistics{}
	if err := stats.SetTrustedProxies([]string{"127.0.0.1"}); err != nil {
		t.Fatalf("SetTrustedProxies() error = %v", err)
	}
	ctx := clientAddressContext(internallogging.RequestClientAddress{
		RemoteAddr: "127.0.0.1:8080",
		XRealIP:    "2001:db8::7",
	})

	if got := stats.resolveClientIP(ctx); got != "2001:db8::7" {
		t.Fatalf("resolveClientIP() = %q, want X-Real-IP", got)
	}
}

func TestSetTrustedProxiesRejectsInvalidEntries(t *testing.T) {
	stats := &RequestStatistics{}
	if err := stats.SetTrustedProxies([]string{"10.0.0.0/8", "invalid"}); err == nil {
		t.Fatal("SetTrustedProxies() error = nil, want invalid entry error")
	}
	if !stats.isTrustedProxy(mustParseClientIP(t, "10.2.3.4")) {
		t.Fatal("valid trusted proxy was not retained")
	}
}

func TestSQLiteDetailStoreMigratesClientIPColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite3")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE usage_details (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		api_name TEXT NOT NULL,
		model_name TEXT NOT NULL,
		timestamp_ns INTEGER NOT NULL,
		latency_ms INTEGER NOT NULL,
		source TEXT NOT NULL,
		auth_index TEXT NOT NULL,
		reasoning_effort TEXT NOT NULL DEFAULT '',
		input_tokens INTEGER NOT NULL,
		output_tokens INTEGER NOT NULL,
		reasoning_tokens INTEGER NOT NULL,
		cached_tokens INTEGER NOT NULL,
		total_tokens INTEGER NOT NULL,
		failed INTEGER NOT NULL,
		dedup_key TEXT NOT NULL UNIQUE
	)`)
	if err != nil {
		t.Fatalf("create old schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close old sqlite: %v", err)
	}

	store, err := newSQLiteDetailStore(path)
	if err != nil {
		t.Fatalf("newSQLiteDetailStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.db.Close() })
	inserted, err := store.Insert("sk-test", "gpt-test", RequestDetail{
		Timestamp:    time.Date(2026, 7, 23, 1, 2, 3, 0, time.UTC),
		ClientIP:     "198.51.100.9",
		ServiceTier:  "fast",
		AppliedTier:  "priority",
		ResponseTier: "priority",
		Tokens:       TokenStats{TotalTokens: 1},
	})
	if err != nil || !inserted {
		t.Fatalf("Insert() = %v, %v, want true, nil", inserted, err)
	}
	page, err := store.Page(DetailPageQuery{PageSize: 10})
	if err != nil {
		t.Fatalf("Page() error = %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ClientIP != "198.51.100.9" {
		t.Fatalf("Page() items = %#v, want migrated client IP", page.Items)
	}
	if item := page.Items[0]; item.ServiceTier != "fast" || item.AppliedTier != "priority" || item.ResponseTier != "priority" {
		t.Fatalf("Page() service tiers = %#v", item)
	}
}

func clientAddressContext(address internallogging.RequestClientAddress) context.Context {
	return internallogging.WithRequestClientAddress(context.Background(), address)
}

func mustParseClientIP(t *testing.T, value string) netip.Addr {
	t.Helper()
	address, ok := parseClientIP(value)
	if !ok {
		t.Fatalf("parseClientIP(%q) failed", value)
	}
	return address
}
