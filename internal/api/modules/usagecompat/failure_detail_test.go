package usagecompat

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestFailureDetailPersistsOnlyForFailedRequests(t *testing.T) {
	previous := redisqueue.UsageStatisticsEnabled()
	redisqueue.SetUsageStatisticsEnabled(true)
	t.Cleanup(func() { redisqueue.SetUsageStatisticsEnabled(previous) })
	stats := newTestRequestStatistics(t)
	now := time.Date(2026, 9, 24, 1, 6, 0, 0, time.UTC)
	stats.Record(context.Background(), coreusage.Record{
		APIKey: "key", Model: "model", RequestedAt: now, Failed: true,
		Fail: coreusage.Failure{StatusCode: 503, Body: "utls: TLS handshake: EOF\nauthorization: Bearer secret-token\ncookie: session=secret"},
	})
	stats.Record(context.Background(), coreusage.Record{
		APIKey: "key", Model: "model", RequestedAt: now.Add(time.Second),
		Fail: coreusage.Failure{StatusCode: 500, Body: "must not persist"},
	})
	page := stats.DetailsPage(DetailPageQuery{PageSize: 10})
	if len(page.Items) != 2 || page.Items[0].ErrorMessage != "" || page.Items[0].ErrorStatus != 0 {
		t.Fatalf("success row retained error: %+v", page.Items)
	}
	if page.Items[1].ErrorStatus != 503 || page.Items[1].ErrorMessage != "utls: TLS handshake: EOF\nauthorization: \"[REDACTED]\"\ncookie: [REDACTED]" {
		t.Fatalf("failure row lost error: %+v", page.Items[1])
	}
	dashboard := NewHandler(stats, nil).dashboardSnapshot(nil)
	for _, api := range dashboard.APIs {
		for _, model := range api.Models {
			for _, detail := range model.Details {
				if detail.ErrorStatus != 0 || detail.ErrorMessage != "" {
					t.Fatal("dashboard summary exposed failure detail")
				}
			}
		}
	}
}
