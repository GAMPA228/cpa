package apikeyquota

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestManagerCheckRejectsAfterDailyLimitReached(t *testing.T) {
	store, err := newSQLiteStore(filepath.Join(t.TempDir(), "quota.sqlite3"))
	if err != nil {
		t.Fatalf("newSQLiteStore() error = %v", err)
	}
	manager := &Manager{
		cfg: &config.Config{SDKConfig: config.SDKConfig{APIKeyEntries: config.APIKeyEntryList{
			{APIKey: "sk-test", DailyTokenLimit: 100},
		}}},
		store: store,
		loc:   time.UTC,
	}
	t.Cleanup(func() { _ = manager.Close() })
	now := time.Date(2026, 6, 30, 10, 0, 0, 0, time.UTC)

	allowed, err := manager.Check("sk-test", now)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if !allowed.Allowed || allowed.UsedTokens != 0 || allowed.RemainingTokens != 100 {
		t.Fatalf("initial decision = %#v, want allowed with 100 remaining", allowed)
	}

	manager.HandleUsage(context.Background(), coreusage.Record{
		APIKey:      "sk-test",
		RequestedAt: now,
		Detail:      coreusage.Detail{TotalTokens: 120},
	})

	blocked, err := manager.Check("sk-test", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("Check() after usage error = %v", err)
	}
	if blocked.Allowed {
		t.Fatalf("blocked decision allowed = true, want false: %#v", blocked)
	}
	if blocked.UsedTokens != 120 || blocked.RemainingTokens != 0 {
		t.Fatalf("blocked usage = used %d remaining %d, want 120/0", blocked.UsedTokens, blocked.RemainingTokens)
	}
}

func TestManagerStatusesIncludeConfiguredKeysAndResetNextDay(t *testing.T) {
	store, err := newSQLiteStore(filepath.Join(t.TempDir(), "quota.sqlite3"))
	if err != nil {
		t.Fatalf("newSQLiteStore() error = %v", err)
	}
	manager := &Manager{
		cfg: &config.Config{SDKConfig: config.SDKConfig{APIKeyEntries: config.APIKeyEntryList{
			{APIKey: "sk-limited", Remark: "Alice", DailyTokenLimit: 100},
			{APIKey: "sk-unlimited", Remark: "Bob"},
		}}},
		store: store,
		loc:   time.UTC,
	}
	t.Cleanup(func() { _ = manager.Close() })
	now := time.Date(2026, 6, 30, 23, 30, 0, 0, time.UTC)
	manager.HandleUsage(context.Background(), coreusage.Record{
		APIKey:      "sk-limited",
		RequestedAt: now,
		Detail:      coreusage.Detail{InputTokens: 40, OutputTokens: 20},
	})

	statuses, err := manager.Statuses(now)
	if err != nil {
		t.Fatalf("Statuses() error = %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("statuses len = %d, want 2: %#v", len(statuses), statuses)
	}
	limited := statuses[0]
	if limited.APIKey != "sk-limited" || limited.Remark != "Alice" || limited.UsedTokens != 60 || limited.RemainingTokens != 40 {
		t.Fatalf("limited status = %#v, want Alice used 60 remaining 40", limited)
	}
	wantReset := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if !limited.ResetAt.Equal(wantReset) {
		t.Fatalf("reset at = %s, want %s", limited.ResetAt, wantReset)
	}
	if statuses[1].Limited || statuses[1].RemainingTokens != 0 {
		t.Fatalf("unlimited status = %#v, want unlimited", statuses[1])
	}
}

func TestManagerUsageStaysInMemoryUntilFlush(t *testing.T) {
	store, err := newSQLiteStore(filepath.Join(t.TempDir(), "quota.sqlite3"))
	if err != nil {
		t.Fatalf("newSQLiteStore() error = %v", err)
	}
	manager := &Manager{
		cfg: &config.Config{SDKConfig: config.SDKConfig{APIKeyEntries: config.APIKeyEntryList{
			{APIKey: "sk-test", DailyTokenLimit: 100},
		}}},
		store: store,
		loc:   time.UTC,
	}
	t.Cleanup(func() { _ = manager.Close() })
	now := time.Date(2026, 6, 30, 10, 0, 0, 0, time.UTC)

	manager.HandleUsage(context.Background(), coreusage.Record{
		APIKey:      "sk-test",
		RequestedAt: now,
		Detail:      coreusage.Detail{TotalTokens: 75},
	})

	decision, err := manager.Check("sk-test", now)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if decision.UsedTokens != 75 || decision.RemainingTokens != 25 {
		t.Fatalf("in-memory decision = %#v, want used 75 remaining 25", decision)
	}

	storedBeforeFlush, err := store.Get(hashAPIKey("sk-test"), "2026-06-30")
	if err != nil {
		t.Fatalf("store.Get() before flush error = %v", err)
	}
	if storedBeforeFlush.UsedTokens != 0 || storedBeforeFlush.RequestCount != 0 {
		t.Fatalf("stored before flush = %#v, want zero", storedBeforeFlush)
	}

	if err := manager.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	storedAfterFlush, err := store.Get(hashAPIKey("sk-test"), "2026-06-30")
	if err != nil {
		t.Fatalf("store.Get() after flush error = %v", err)
	}
	if storedAfterFlush.UsedTokens != 75 || storedAfterFlush.RequestCount != 1 {
		t.Fatalf("stored after flush = %#v, want 75 tokens and 1 request", storedAfterFlush)
	}
}

func TestManagerLookupReturnsSingleAPIKeyStatus(t *testing.T) {
	store, err := newSQLiteStore(filepath.Join(t.TempDir(), "quota.sqlite3"))
	if err != nil {
		t.Fatalf("newSQLiteStore() error = %v", err)
	}
	manager := &Manager{
		cfg: &config.Config{SDKConfig: config.SDKConfig{APIKeyEntries: config.APIKeyEntryList{
			{APIKey: "sk-target", Remark: "Alice", DailyTokenLimit: 100},
			{APIKey: "sk-other", Remark: "Bob", DailyTokenLimit: 200},
		}}},
		store: store,
		loc:   time.UTC,
	}
	t.Cleanup(func() { _ = manager.Close() })
	now := time.Date(2026, 6, 30, 10, 0, 0, 0, time.UTC)
	manager.HandleUsage(context.Background(), coreusage.Record{
		APIKey:      "sk-target",
		RequestedAt: now,
		Detail:      coreusage.Detail{TotalTokens: 40},
	})

	status, found, err := manager.Lookup("sk-target", now)
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if !found {
		t.Fatalf("Lookup() found = false, want true")
	}
	if status.APIKey != "sk-target" || status.Remark != "Alice" || status.UsedTokens != 40 || status.RemainingTokens != 60 {
		t.Fatalf("Lookup() status = %#v, want target/Alice/40/60", status)
	}

	_, found, err = manager.Lookup("sk-missing", now)
	if err != nil {
		t.Fatalf("Lookup() missing error = %v", err)
	}
	if found {
		t.Fatalf("Lookup() missing found = true, want false")
	}
}

func TestManagerHistoryReturnsSevenDaysOldestFirst(t *testing.T) {
	store, err := newSQLiteStore(filepath.Join(t.TempDir(), "quota.sqlite3"))
	if err != nil {
		t.Fatalf("newSQLiteStore() error = %v", err)
	}
	manager := &Manager{
		cfg: &config.Config{SDKConfig: config.SDKConfig{APIKeyEntries: config.APIKeyEntryList{
			{APIKey: "sk-history"},
		}}},
		store: store,
		loc:   time.UTC,
	}
	t.Cleanup(func() { _ = manager.Close() })
	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	manager.HandleUsage(context.Background(), coreusage.Record{
		APIKey:      "sk-history",
		RequestedAt: now.AddDate(0, 0, -6),
		Detail:      coreusage.Detail{TotalTokens: 10},
	})
	manager.HandleUsage(context.Background(), coreusage.Record{
		APIKey:      "sk-history",
		RequestedAt: now,
		Detail:      coreusage.Detail{TotalTokens: 70},
	})

	history, found, err := manager.History("sk-history", now, 7)
	if err != nil {
		t.Fatalf("History() error = %v", err)
	}
	if !found {
		t.Fatal("History() found = false, want true")
	}
	if len(history) != 7 {
		t.Fatalf("History() len = %d, want 7", len(history))
	}
	if history[0].Day != "2026-07-01" || history[0].UsedTokens != 10 || history[0].RequestCount != 1 {
		t.Fatalf("History() first = %#v, want 2026-07-01/10/1", history[0])
	}
	if history[6].Day != "2026-07-07" || history[6].UsedTokens != 70 || history[6].RequestCount != 1 {
		t.Fatalf("History() last = %#v, want 2026-07-07/70/1", history[6])
	}

	_, found, err = manager.History("sk-missing", now, 7)
	if err != nil {
		t.Fatalf("History() missing error = %v", err)
	}
	if found {
		t.Fatal("History() missing found = true, want false")
	}
}

type testExternalUsageProvider struct {
	usage map[string]ExternalUsage
	err   error
}

func (p testExternalUsageProvider) APIKeyUsageForDay(time.Time) (map[string]ExternalUsage, error) {
	return p.usage, p.err
}

func TestManagerStatusesBackfillFromExternalUsageProvider(t *testing.T) {
	store, err := newSQLiteStore(filepath.Join(t.TempDir(), "quota.sqlite3"))
	if err != nil {
		t.Fatalf("newSQLiteStore() error = %v", err)
	}
	manager := &Manager{
		cfg: &config.Config{SDKConfig: config.SDKConfig{APIKeyEntries: config.APIKeyEntryList{
			{APIKey: "sk-target", Remark: "Alice", DailyTokenLimit: 200},
		}}},
		store:             store,
		loc:               time.UTC,
		externalSyncEvery: 0,
	}
	manager.initUsageMaps()
	manager.SetExternalUsageProvider(testExternalUsageProvider{usage: map[string]ExternalUsage{
		"sk-target": {UsedTokens: 150, RequestCount: 3},
	}})
	t.Cleanup(func() { _ = manager.Close() })
	now := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)

	statuses, err := manager.Statuses(now)
	if err != nil {
		t.Fatalf("Statuses() error = %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("statuses len = %d, want 1", len(statuses))
	}
	if statuses[0].UsedTokens != 150 || statuses[0].RemainingTokens != 50 || statuses[0].RequestCount != 3 {
		t.Fatalf("status = %#v, want used 150 remaining 50 count 3", statuses[0])
	}

	if err := manager.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	stored, err := store.Get(hashAPIKey("sk-target"), "2026-07-01")
	if err != nil {
		t.Fatalf("store.Get() error = %v", err)
	}
	if stored.UsedTokens != 150 || stored.RequestCount != 3 {
		t.Fatalf("stored = %#v, want 150/3", stored)
	}
}

func TestManagerExternalUsageBackfillDoesNotDoubleCount(t *testing.T) {
	store, err := newSQLiteStore(filepath.Join(t.TempDir(), "quota.sqlite3"))
	if err != nil {
		t.Fatalf("newSQLiteStore() error = %v", err)
	}
	manager := &Manager{
		cfg: &config.Config{SDKConfig: config.SDKConfig{APIKeyEntries: config.APIKeyEntryList{
			{APIKey: "sk-target", DailyTokenLimit: 500},
		}}},
		store:             store,
		loc:               time.UTC,
		externalSyncEvery: 0,
	}
	manager.initUsageMaps()
	provider := testExternalUsageProvider{usage: map[string]ExternalUsage{
		"sk-target": {UsedTokens: 150, RequestCount: 3},
	}}
	manager.SetExternalUsageProvider(provider)
	t.Cleanup(func() { _ = manager.Close() })
	now := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)

	if _, err := manager.Statuses(now); err != nil {
		t.Fatalf("first Statuses() error = %v", err)
	}
	if _, err := manager.Statuses(now.Add(time.Second)); err != nil {
		t.Fatalf("second Statuses() error = %v", err)
	}
	if err := manager.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	stored, err := store.Get(hashAPIKey("sk-target"), "2026-07-01")
	if err != nil {
		t.Fatalf("store.Get() error = %v", err)
	}
	if stored.UsedTokens != 150 || stored.RequestCount != 3 {
		t.Fatalf("stored after repeated sync = %#v, want 150/3", stored)
	}
}
