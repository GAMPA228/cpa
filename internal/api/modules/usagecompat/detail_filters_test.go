package usagecompat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestDetailResultAndTimeFilters(t *testing.T) {
	start := time.Date(2026, 9, 20, 10, 30, 0, 0, time.UTC)
	end := start.Add(2 * time.Second)
	details := []RequestDetail{
		{Timestamp: start.Add(-time.Nanosecond)},
		{Timestamp: start},
		{Timestamp: start.Add(500 * time.Millisecond), Failed: true},
		{Timestamp: end.Add(-time.Nanosecond), Failed: true},
		{Timestamp: end},
		{Timestamp: end.Add(time.Second), Failed: true},
	}
	for i := range details {
		details[i].Source = "account"
		details[i].AuthIndex = "42"
	}
	for _, persistent := range []bool{true, false} {
		name := "sqlite"
		if !persistent {
			name = "memory"
		}
		t.Run(name, func(t *testing.T) {
			stats := newTestRequestStatistics(t)
			if stats.detailStore == nil {
				t.Fatal("test SQLite unavailable")
			}
			if !persistent {
				if err := stats.detailStore.db.Close(); err != nil {
					t.Fatal(err)
				}
				stats.detailStore = nil
			}
			result := stats.MergeSnapshot(StatisticsSnapshot{APIs: map[string]APISnapshot{
				"key-a": {Models: map[string]ModelSnapshot{
					"model-a": {Details: details},
					"model-b": {Details: []RequestDetail{{Timestamp: start, Failed: true}}},
				}},
			}})
			if result.Added != 7 {
				t.Fatalf("imported %d, want 7", result.Added)
			}
			router := gin.New()
			router.GET("/details", NewHandler(stats, nil).GetUsageDetails)
			call := func(params url.Values) DetailPage {
				t.Helper()
				w := httptest.NewRecorder()
				router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/details?"+params.Encode(), nil))
				if w.Code != http.StatusOK {
					t.Fatalf("response %d: %s", w.Code, w.Body.String())
				}
				var page DetailPage
				if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
					t.Fatal(err)
				}
				return page
			}
			params := url.Values{
				"start_time": {"2026-09-20T18:30:00+08:00"},
				"end_time":   {"2026-09-20T18:30:02+08:00"},
				"result":     {"failed"}, "model": {"model-a"}, "api": {"key-a"},
				"source": {"account"}, "auth_index": {"42"}, "search": {"model-a"}, "page_size": {"1"},
			}
			page := call(params)
			if page.Total != 2 || len(page.Items) != 1 || !page.HasMore || !page.Items[0].Timestamp.Equal(end.Add(-time.Nanosecond)) {
				t.Fatalf("first page = %+v", page)
			}
			params.Set("page", "2")
			page = call(params)
			if page.Total != 2 || page.HasMore || len(page.Items) != 1 || !page.Items[0].Timestamp.Equal(start.Add(500*time.Millisecond)) {
				t.Fatalf("second page = %+v", page)
			}
			params.Set("page", "3")
			if page = call(params); page.Total != 2 || len(page.Items) != 0 {
				t.Fatal("pagination lost filtered total")
			}
			params.Set("page", "1")
			params.Set("result", "success")
			if page = call(params); page.Total != 1 || len(page.Items) != 1 || !page.Items[0].Timestamp.Equal(start) {
				t.Fatal("success or lower boundary incorrect")
			}
			params.Set("result", "all")
			if page = call(params); page.Total != 3 {
				t.Fatal("all results did not preserve time range")
			}
			params.Del("start_time")
			if page = call(params); page.Total != 4 {
				t.Fatal("end-only query incorrect")
			}
			params.Del("end_time")
			params.Set("start_time", start.Format(time.RFC3339Nano))
			if page = call(params); page.Total != 5 {
				t.Fatal("start-only query incorrect")
			}
			params.Del("start_time")
			params.Del("result")
			if page = call(params); page.Total != 6 {
				t.Fatal("legacy query changed")
			}
			params.Set("result", "failed")
			params.Set("search", "no-match")
			if page = call(params); page.Total != 0 || len(page.Items) != 0 {
				t.Fatal("filters not combined")
			}
		})
	}
}

func TestDetailFilterInvalidParameters(t *testing.T) {
	stats := newTestRequestStatistics(t)
	router := gin.New()
	router.GET("/details", NewHandler(stats, nil).GetUsageDetails)
	for _, params := range []url.Values{
		{"result": {"maybe"}},
		{"start_time": {"2026-09-20T18:30:00"}},
		{"start_time": {"not-a-date"}},
		{"end_time": {"2026-02-30T00:00:00Z"}},
		{"start_time": {"0001-01-01T00:00:00Z"}},
		{"end_time": {"9999-12-31T00:00:00Z"}},
		{"start_time": {"2026-09-20T18:30:00Z"}, "end_time": {"2026-09-20T18:30:00Z"}},
		{"start_time": {"2026-09-20T18:31:00Z"}, "end_time": {"2026-09-20T18:30:00Z"}},
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/details?"+params.Encode(), nil))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("accepted %v: %d", params, w.Code)
		}
	}
}

func TestDetailFilterIndexSupportsExistingRows(t *testing.T) {
	stats := newTestRequestStatistics(t)
	store := stats.detailStore
	if store == nil {
		t.Fatal("test SQLite unavailable")
	}
	if _, err := store.Insert("key", "model", RequestDetail{Timestamp: time.Now(), Failed: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("DROP INDEX usage_details_failed_time_idx"); err != nil {
		t.Fatal(err)
	}
	if err := store.init(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM usage_details").Scan(&count); err != nil || count != 1 {
		t.Fatal("index upgrade changed historical rows")
	}
	rows, err := store.db.Query("EXPLAIN QUERY PLAN SELECT id FROM usage_details WHERE failed=1 AND timestamp_ns >= ? AND timestamp_ns < ? ORDER BY timestamp_ns DESC,id DESC LIMIT 10", int64(0), time.Now().Add(time.Hour).UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	used := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(detail, "usage_details_failed_time_idx") {
			used = true
		}
		if strings.Contains(detail, "TEMP B-TREE") {
			t.Fatal("filtered pagination unnecessarily sorts all records")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !used {
		t.Fatal("result/time index not used")
	}
}
