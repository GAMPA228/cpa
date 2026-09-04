package usagecompat

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestQuotaEstimatorOverviewCalculatesRemainingUSD(t *testing.T) {
	previousEnabled := redisqueue.UsageStatisticsEnabled()
	redisqueue.SetUsageStatisticsEnabled(true)
	t.Cleanup(func() { redisqueue.SetUsageStatisticsEnabled(previousEnabled) })
	t.Setenv(usageSQLitePathEnv, filepath.Join(t.TempDir(), "usage.sqlite3"))
	stats := NewRequestStatistics()
	t.Cleanup(func() { _ = stats.detailStore.db.Close() })

	now := time.Now().UTC().Truncate(time.Minute)
	resetAt := now.Add(4 * time.Hour).Unix()
	for index, used := range []string{"10", "20"} {
		headers := http.Header{}
		headers.Set("X-Codex-Primary-Used-Percent", used)
		headers.Set("X-Codex-Primary-Reset-At", formatQuotaEstimatorInt(resetAt))
		headers.Set("X-Codex-Primary-Window-Minutes", "300")
		headers.Set("X-Codex-Plan-Type", "plus")
		stats.Record(context.Background(), coreusage.Record{
			Provider:    "codex",
			Model:       "custom-model",
			AuthID:      "account-a",
			AuthIndex:   "auth-index-a",
			RequestedAt: now.Add(time.Duration(index) * time.Minute),
			Detail: coreusage.Detail{
				InputTokens:  100,
				OutputTokens: 0,
				TotalTokens:  100,
			},
			ResponseHeaders: headers,
		})
	}

	overview, err := stats.QuotaEstimatorOverview(QuotaEstimatorOptions{
		Prices: map[string]QuotaEstimatorPrice{
			"custom-model": {Prompt: 1, Completion: 0, Cache: 0},
		},
	})
	if err != nil {
		t.Fatalf("QuotaEstimatorOverview() error = %v", err)
	}
	if len(overview.Accounts) != 1 {
		t.Fatalf("accounts = %d, want 1", len(overview.Accounts))
	}
	window := overview.Accounts[0].Primary
	if !window.EstimateAvailable || window.SampleCount != 1 {
		t.Fatalf("estimate = %+v, want one available interval", window)
	}
	if math.Abs(window.FullWindowCostUSD-0.001) > 0.0000001 {
		t.Fatalf("full window cost = %f, want 0.001", window.FullWindowCostUSD)
	}
	if math.Abs(window.RemainingCostUSD-0.0008) > 0.0000001 {
		t.Fatalf("remaining cost = %f, want 0.0008", window.RemainingCostUSD)
	}
	if window.CurrentCycleTokens != 200 {
		t.Fatalf("current cycle tokens = %d, want 200", window.CurrentCycleTokens)
	}
}

func TestQuotaEstimatorOverviewReportsMissingPrice(t *testing.T) {
	t.Setenv(usageSQLitePathEnv, filepath.Join(t.TempDir(), "usage.sqlite3"))
	stats := NewRequestStatistics()
	t.Cleanup(func() { _ = stats.detailStore.db.Close() })
	now := time.Now().UTC().Truncate(time.Minute)
	headers := http.Header{
		"X-Codex-Primary-Used-Percent":   {"10"},
		"X-Codex-Primary-Reset-At":       {formatQuotaEstimatorInt(now.Add(time.Hour).Unix())},
		"X-Codex-Primary-Window-Minutes": {"300"},
	}
	if err := stats.detailStore.insertQuotaEstimatorEvent(coreusage.Record{
		Provider: "codex", Model: "unpriced-model", AuthID: "account-a", RequestedAt: now,
		Detail: coreusage.Detail{InputTokens: 10, TotalTokens: 10}, ResponseHeaders: headers,
	}); err != nil {
		t.Fatalf("insertQuotaEstimatorEvent() error = %v", err)
	}
	overview, err := stats.QuotaEstimatorOverview(QuotaEstimatorOptions{})
	if err != nil {
		t.Fatalf("QuotaEstimatorOverview() error = %v", err)
	}
	if len(overview.MissingPriceModels) != 1 || overview.MissingPriceModels[0] != "unpriced-model" {
		t.Fatalf("missing prices = %#v", overview.MissingPriceModels)
	}
}

func TestQuotaEstimatorResetAfterSecondsProducesStableResetTime(t *testing.T) {
	observedAt := time.Unix(1_800_000_010, 0).UTC()
	headers := http.Header{
		"X-Codex-Primary-Reset-After-Seconds": {"299"},
	}
	got := quotaResetAt(headers, "X-Codex-Primary-", observedAt)
	want := canonicalQuotaResetAt(observedAt.Unix() + 299)
	if got != want {
		t.Fatalf("quotaResetAt() = %d, want %d", got, want)
	}
}

func formatQuotaEstimatorInt(value int64) string {
	return fmt.Sprintf("%d", value)
}
