package usagecompat

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestFirstTokenRecordPersistenceAndAPI(t *testing.T) {
	previous := redisqueue.UsageStatisticsEnabled()
	redisqueue.SetUsageStatisticsEnabled(true)
	t.Cleanup(func() { redisqueue.SetUsageStatisticsEnabled(previous) })
	stats := newTestRequestStatistics(t)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	for i, ttft := range []time.Duration{1250 * time.Millisecond, 0, 500 * time.Microsecond, -time.Second} {
		stats.Record(context.Background(), coreusage.Record{
			APIKey: "demo-key", Model: "gpt-test", Provider: "codex",
			RequestedAt: now.Add(time.Duration(i) * time.Second),
			Latency:     10 * time.Second, TTFT: ttft,
			Detail: coreusage.Detail{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		})
	}
	assertFirstTokenSnapshot(t, stats.Snapshot())
	if err := stats.detailStore.db.Close(); err != nil {
		t.Fatal(err)
	}
	restored := NewRequestStatistics()
	t.Cleanup(func() { _ = restored.detailStore.db.Close() })
	assertFirstTokenSnapshot(t, restored.Snapshot())

	router := gin.New()
	if err := New(NewHandler(restored, nil)).Register(router); err != nil {
		t.Fatal(err)
	}
	page := performJSONRequest(t, router, http.MethodGet, "/v0/management/usage/details?api=demo-key", nil)
	items := page["items"].([]any)
	if len(items) != 4 {
		t.Fatalf("items = %d, want 4", len(items))
	}
	for i, want := range []any{nil, float64(0), nil, float64(1250)} {
		row := items[i].(map[string]any)
		got, exists := row["first_token_ms"]
		if !exists || got != want {
			t.Fatalf("row %d first_token_ms = %v, present %v, want %v", i, got, exists, want)
		}
		if row["latency_ms"] != float64(10000) {
			t.Fatalf("total latency changed: %v", row)
		}
	}

	data, err := json.Marshal(restored.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var exported StatisticsSnapshot
	if err := json.Unmarshal(data, &exported); err != nil {
		t.Fatal(err)
	}
	imported := newTestRequestStatistics(t)
	if result := imported.MergeSnapshot(exported); result.Added != 4 {
		t.Fatalf("import result = %+v", result)
	}
	assertFirstTokenSnapshot(t, imported.Snapshot())
	if result := imported.MergeSnapshot(exported); result.Added != 0 || result.Skipped != 4 {
		t.Fatalf("repeat import changed deduplication: %+v", result)
	}
}

func assertFirstTokenSnapshot(t *testing.T, snapshot StatisticsSnapshot) {
	t.Helper()
	details := snapshot.APIs["demo-key"].Models["gpt-test"].Details
	if len(details) != 4 {
		t.Fatalf("details = %d, want 4", len(details))
	}
	for i, want := range []int64{1250, -1, 0, -1} {
		got := details[i].FirstTokenMs
		if (want < 0 && got != nil) || (want >= 0 && (got == nil || *got != want)) {
			t.Fatalf("detail %d first_token_ms = %v, want %d (negative means absent)", i, got, want)
		}
	}
}

func TestFirstTokenMigratesExistingRows(t *testing.T) {
	stats := newTestRequestStatistics(t)
	store := stats.detailStore
	_, err := store.Insert("demo-key", "gpt-test", RequestDetail{Timestamp: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`ALTER TABLE usage_details DROP COLUMN first_token_ms`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := store.init(); err != nil {
			t.Fatalf("migration %d: %v", i, err)
		}
	}
	page, err := store.Page(DetailPageQuery{})
	if err != nil || len(page.Items) != 1 || page.Items[0].FirstTokenMs != nil {
		t.Fatalf("legacy row after migration = %+v, %v", page, err)
	}
	negative := int64(-1)
	if normalizeRequestDetail(RequestDetail{FirstTokenMs: &negative}).FirstTokenMs != nil {
		t.Fatal("negative imported TTFT must be treated as absent")
	}
}
