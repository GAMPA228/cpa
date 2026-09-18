package usagecompat

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestTurnStateLengthRoundTrip(t *testing.T) {
	previous := redisqueue.UsageStatisticsEnabled()
	redisqueue.SetUsageStatisticsEnabled(true)
	t.Cleanup(func() { redisqueue.SetUsageStatisticsEnabled(previous) })
	stats := newTestRequestStatistics(t)
	zero, positive, negative := 0, 123, -1
	for i, length := range []*int{nil, &zero, &positive, &negative} {
		stats.Record(context.Background(), coreusage.Record{
			APIKey: "key", Model: "model", TurnStateLength: length,
			RequestedAt: time.Date(2026, 9, 18, 0, 0, i, 0, time.UTC),
			Detail:      coreusage.Detail{InputTokens: 7, TotalTokens: 7},
		})
	}
	assertStats := func(s *RequestStatistics) {
		t.Helper()
		for _, limit := range []int{SnapshotAllDetails, 4} {
			snapshot := s.SnapshotWithOptions(SnapshotOptions{DetailLimit: limit})
			details := snapshot.APIs["key"].Models["model"].Details
			if len(details) != 4 || snapshot.TotalTokens != 28 {
				t.Fatalf("unexpected snapshot: %+v", snapshot)
			}
			for i, want := range []*int{nil, &zero, &positive, nil} {
				assertTurnStateLength(t, details[i].TurnStateLength, want)
			}
		}
		page := s.DetailsPage(DetailPageQuery{})
		if len(page.Items) != 4 {
			t.Fatalf("page = %+v", page)
		}
		for i, want := range []any{nil, float64(123), float64(0), nil} {
			data, err := json.Marshal(page.Items[i])
			if err != nil {
				t.Fatal(err)
			}
			var row map[string]any
			if err := json.Unmarshal(data, &row); err != nil {
				t.Fatal(err)
			}
			if got, exists := row["turn_state_length"]; !exists || got != want {
				t.Fatalf("row length = %v, present %v, want %v", got, exists, want)
			}
		}
	}
	assertStats(stats)
	if err := stats.detailStore.db.Close(); err != nil {
		t.Fatal(err)
	}
	restored := NewRequestStatistics()
	if restored.detailStore == nil {
		t.Fatal("reopened store unavailable")
	}
	t.Cleanup(func() { _ = restored.detailStore.db.Close() })
	assertStats(restored)
	data, err := json.Marshal(restored.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	for _, persistent := range []bool{true, false} {
		var exported StatisticsSnapshot
		if err := json.Unmarshal(data, &exported); err != nil {
			t.Fatal(err)
		}
		imported := newTestRequestStatistics(t)
		if !persistent {
			if err := imported.detailStore.db.Close(); err != nil {
				t.Fatal(err)
			}
			imported.detailStore = nil
		}
		if result := imported.MergeSnapshot(exported); result.Added != 4 {
			t.Fatalf("import: %+v", result)
		}
		assertStats(imported)
		for _, api := range exported.APIs {
			for _, model := range api.Models {
				for i := range model.Details {
					model.Details[i].TurnStateLength = nil
				}
			}
		}
		if result := imported.MergeSnapshot(exported); result.Added != 0 || result.Skipped != 4 {
			t.Fatalf("dedup changed: %+v", result)
		}
		assertStats(imported)
	}
}

func TestTurnStateLengthMigration(t *testing.T) {
	store := newTestRequestStatistics(t).detailStore
	detail := RequestDetail{Timestamp: time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC), ResponseModel: "actual", Tokens: TokenStats{TotalTokens: 7}}
	if inserted, err := store.Insert("key", "model", detail); err != nil || !inserted {
		t.Fatalf("insert: %v, %v", inserted, err)
	}
	if _, err := store.db.Exec(`ALTER TABLE usage_details DROP COLUMN turn_state_length`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := store.init(); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.Page(DetailPageQuery{})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("page: %+v, %v", page, err)
	}
	row := page.Items[0]
	if row.TurnStateLength != nil || row.ResponseModel != "actual" || row.Tokens.TotalTokens != 7 {
		t.Fatalf("legacy data changed: %+v", row)
	}
	value := 42
	detail.TurnStateLength = &value
	if inserted, err := store.Insert("key", "model", detail); err != nil || inserted {
		t.Fatalf("legacy dedup changed: %v, %v", inserted, err)
	}
	detail.Timestamp = detail.Timestamp.Add(time.Second)
	if inserted, err := store.Insert("key", "model", detail); err != nil || !inserted {
		t.Fatalf("new insert: %v, %v", inserted, err)
	}
	page, err = store.Page(DetailPageQuery{})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("page: %+v, %v", page, err)
	}
	assertTurnStateLength(t, page.Items[0].TurnStateLength, &value)
	var historical RequestDetail
	if err := json.Unmarshal([]byte(`{"timestamp":"2026-09-18T00:00:00Z"}`), &historical); err != nil {
		t.Fatal(err)
	}
	assertTurnStateLength(t, historical.TurnStateLength, nil)
	value = -1
	assertTurnStateLength(t, normalizeRequestDetail(detail).TurnStateLength, nil)
}

func assertTurnStateLength(t *testing.T, got, want *int) {
	t.Helper()
	if want == nil && got != nil || want != nil && (got == nil || *got != *want) {
		t.Fatalf("length = %v, want %v", got, want)
	}
}
