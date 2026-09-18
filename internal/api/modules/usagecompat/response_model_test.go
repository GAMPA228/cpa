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

func TestResponseModelRecordPersistenceAndAPI(t *testing.T) {
	previous := redisqueue.UsageStatisticsEnabled()
	redisqueue.SetUsageStatisticsEnabled(true)
	t.Cleanup(func() { redisqueue.SetUsageStatisticsEnabled(previous) })
	stats := newTestRequestStatistics(t)
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	cases := []struct{ record, detail, want string }{
		{" upstream-version ", "ignored", "upstream-version"},
		{" \t", " upstream-fallback ", "upstream-fallback"},
		{"", "", ""},
	}
	for i, tc := range cases {
		stats.Record(context.Background(), coreusage.Record{
			APIKey: "demo-key", Model: "requested-alias", ResponseModel: tc.record,
			RequestedAt: now.Add(time.Duration(i) * time.Second),
			Detail:      coreusage.Detail{ResponseModel: tc.detail, InputTokens: 10, TotalTokens: 10},
		})
	}
	assertSnapshot := func(stats *RequestStatistics) {
		t.Helper()
		for _, limit := range []int{SnapshotAllDetails, 3} {
			snapshot := stats.SnapshotWithOptions(SnapshotOptions{DetailLimit: limit})
			details := snapshot.APIs["demo-key"].Models["requested-alias"].Details
			if len(details) != len(cases) || snapshot.TotalRequests != 3 {
				t.Fatalf("snapshot = %+v", snapshot)
			}
			for i, tc := range cases {
				if details[i].ResponseModel != tc.want {
					t.Fatalf("detail %d response model = %q, want %q", i, details[i].ResponseModel, tc.want)
				}
			}
		}
	}
	assertSnapshot(stats)
	if err := stats.detailStore.db.Close(); err != nil {
		t.Fatal(err)
	}
	restored := NewRequestStatistics()
	if restored.detailStore == nil {
		t.Fatal("reopened store unavailable")
	}
	t.Cleanup(func() { _ = restored.detailStore.db.Close() })
	assertSnapshot(restored)

	router := gin.New()
	if err := New(NewHandler(restored, nil)).Register(router); err != nil {
		t.Fatal(err)
	}
	page := performJSONRequest(t, router, http.MethodGet, "/v0/management/usage/details?search=UPSTREAM-VERSION", nil)
	items := page["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["response_model"] != cases[0].want || items[0].(map[string]any)["model"] != "requested-alias" {
		t.Fatalf("response model search result = %+v", page)
	}
	data, err := json.Marshal(restored.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var exported StatisticsSnapshot
	if err := json.Unmarshal(data, &exported); err != nil {
		t.Fatal(err)
	}
	for _, persistent := range []bool{true, false} {
		imported := newTestRequestStatistics(t)
		if !persistent {
			if err := imported.detailStore.db.Close(); err != nil {
				t.Fatal(err)
			}
			imported.detailStore = nil
		}
		if result := imported.MergeSnapshot(exported); result.Added != 3 {
			t.Fatalf("import result = %+v", result)
		}
		assertSnapshot(imported)
		page := imported.DetailsPage(DetailPageQuery{Search: "UPSTREAM-FALLBACK"})
		if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ResponseModel != cases[1].want {
			t.Fatalf("imported search result = %+v", page)
		}
		// Older exports omit the metadata but must still identify the same requests.
		var legacy StatisticsSnapshot
		if err := json.Unmarshal(data, &legacy); err != nil {
			t.Fatal(err)
		}
		for _, api := range legacy.APIs {
			for _, model := range api.Models {
				for i := range model.Details {
					model.Details[i].ResponseModel = ""
				}
			}
		}
		if result := imported.MergeSnapshot(legacy); result.Added != 0 || result.Skipped != 3 {
			t.Fatalf("metadata changed deduplication: %+v", result)
		}
		assertSnapshot(imported)
	}
}

func TestResponseModelMigratesExistingRows(t *testing.T) {
	stats := newTestRequestStatistics(t)
	store := stats.detailStore
	detail := RequestDetail{Timestamp: time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)}
	if inserted, err := store.Insert("demo-key", "requested-alias", detail); err != nil || !inserted {
		t.Fatalf("insert: %v, %v", inserted, err)
	}
	if _, err := store.db.Exec(`ALTER TABLE usage_details DROP COLUMN response_model`); err != nil {
		t.Fatal(err)
	}
	if err := store.db.Close(); err != nil {
		t.Fatal(err)
	}
	restored := NewRequestStatistics()
	if restored.detailStore == nil {
		t.Fatal("legacy store migration failed")
	}
	t.Cleanup(func() { _ = restored.detailStore.db.Close() })
	store = restored.detailStore
	if err := store.init(); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	page, err := store.Page(DetailPageQuery{})
	if err != nil || len(page.Items) != 1 || page.Items[0].ResponseModel != "" {
		t.Fatalf("legacy row after migration = %+v, %v", page, err)
	}
	detail.ResponseModel = "actual-model"
	if inserted, err := store.Insert("demo-key", "requested-alias", detail); err != nil || inserted {
		t.Fatalf("metadata changed legacy deduplication: %v, %v", inserted, err)
	}
	detail.Timestamp = detail.Timestamp.Add(time.Second)
	if inserted, err := store.Insert("demo-key", "requested-alias", detail); err != nil || !inserted {
		t.Fatalf("insert after migration: %v, %v", inserted, err)
	}
	page, err = store.Page(DetailPageQuery{Search: "actual-model"})
	if err != nil || len(page.Items) != 1 || page.Items[0].ResponseModel != "actual-model" {
		t.Fatalf("new row after migration = %+v, %v", page, err)
	}
}

func TestResponseModelRowMapping(t *testing.T) {
	detail := RequestDetail{ResponseModel: " actual_%model ", Timestamp: time.Now()}
	row := detailRowFromDetail(1, "api", "requested-alias", detail)
	if row.ResponseModel != "actual_%model" || detailFromRow(row).ResponseModel != "actual_%model" {
		t.Fatalf("row mapping lost response model: %+v", row)
	}
	stats := newTestRequestStatistics(t)
	if _, err := stats.detailStore.Insert("api", "requested-alias", detail); err != nil {
		t.Fatal(err)
	}
	for _, search := range []string{"ACTUAL_%MODEL", "actualXXmodel"} {
		page, err := stats.detailStore.Page(DetailPageQuery{Search: search})
		want := detailRowMatchesQuery(row, DetailPageQuery{Search: search})
		if err != nil || (page.Total == 1) != want {
			t.Fatalf("search %q: page %+v, error %v, want match %v", search, page, err, want)
		}
	}
}
