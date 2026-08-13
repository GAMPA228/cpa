package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	proxyconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestPublicAPIKeyUsageLookupReturnsOwnStatusWithoutManagementAuth(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	t.Setenv("APIKEY_QUOTA_SQLITE_PATH", filepath.Join(t.TempDir(), "quota.sqlite3"))

	server := newTestServer(t)
	t.Cleanup(func() { _ = server.CloseAPIKeyQuotaManager() })
	server.cfg.APIKeyEntries = proxyconfig.APIKeyEntryList{
		{APIKey: "sk-public-user", Remark: "Alice", DailyTokenLimit: 200},
		{APIKey: "sk-public-other", Remark: "Bob", DailyTokenLimit: 200},
	}
	server.cfg.APIKeys = []string{"sk-public-user", "sk-public-other"}
	server.apiKeyQuotaManager.SetConfig(server.cfg)
	now := time.Now()
	server.apiKeyQuotaManager.HandleUsage(context.Background(), coreusage.Record{
		APIKey:      "sk-public-user",
		RequestedAt: now,
		Detail:      coreusage.Detail{TotalTokens: 25},
	})
	server.apiKeyQuotaManager.HandleUsage(context.Background(), coreusage.Record{
		APIKey:      "sk-public-user",
		RequestedAt: now.AddDate(0, 0, -1),
		Detail:      coreusage.Detail{TotalTokens: 10},
	})
	server.apiKeyQuotaManager.HandleUsage(context.Background(), coreusage.Record{
		APIKey:      "sk-public-other",
		RequestedAt: now,
		Detail:      coreusage.Detail{TotalTokens: 75},
	})

	req := httptest.NewRequest(http.MethodPost, publicAPIKeyUsagePath, strings.NewReader(`{"api-key":"sk-public-user"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var payload struct {
		Item publicAPIKeyUsageStatus `json:"item"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v body=%s", err, rr.Body.String())
	}
	if payload.Item.APIKey == "sk-public-user" || payload.Item.APIKey == "" {
		t.Fatalf("public api key = %q, want masked non-empty value", payload.Item.APIKey)
	}
	if payload.Item.Remark != "Alice" {
		t.Fatalf("remark = %q, want Alice", payload.Item.Remark)
	}
	if payload.Item.UsedTokens != 25 || payload.Item.RemainingTokens != 175 || payload.Item.DailyTokenLimit != 200 {
		t.Fatalf("quota = used %d remaining %d limit %d, want 25/175/200", payload.Item.UsedTokens, payload.Item.RemainingTokens, payload.Item.DailyTokenLimit)
	}
	if payload.Item.RequestCount != 1 {
		t.Fatalf("request count = %d, want 1", payload.Item.RequestCount)
	}
	if payload.Item.UsagePercentage != 12.5 {
		t.Fatalf("usage percentage = %v, want 12.5", payload.Item.UsagePercentage)
	}
	if len(payload.Item.History) != 7 {
		t.Fatalf("history len = %d, want 7", len(payload.Item.History))
	}
	if payload.Item.History[5].UsedTokens != 10 || payload.Item.History[6].UsedTokens != 25 {
		t.Fatalf("history tail = %#v, want yesterday/today 10/25", payload.Item.History[5:])
	}
}

func TestPublicAPIKeyUsageLookupMissingKeyReturnsNotFound(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	t.Setenv("APIKEY_QUOTA_SQLITE_PATH", filepath.Join(t.TempDir(), "quota.sqlite3"))

	server := newTestServer(t)
	t.Cleanup(func() { _ = server.CloseAPIKeyQuotaManager() })
	server.cfg.APIKeyEntries = proxyconfig.APIKeyEntryList{{APIKey: "sk-existing", DailyTokenLimit: 100}}
	server.cfg.APIKeys = []string{"sk-existing"}
	server.apiKeyQuotaManager.SetConfig(server.cfg)

	req := httptest.NewRequest(http.MethodPost, publicAPIKeyUsagePath, strings.NewReader(`{"api-key":"sk-missing"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusNotFound, rr.Body.String())
	}
}

func TestPublicAPIKeyUsagePageServedWithoutManagementAuth(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	t.Setenv("APIKEY_QUOTA_SQLITE_PATH", filepath.Join(t.TempDir(), "quota.sqlite3"))

	server := newTestServer(t)
	t.Cleanup(func() { _ = server.CloseAPIKeyQuotaManager() })

	req := httptest.NewRequest(http.MethodGet, apiKeyUsagePagePath, nil)
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if contentType := rr.Header().Get("Content-Type"); !strings.Contains(contentType, "text/html") {
		t.Fatalf("content type = %q, want text/html", contentType)
	}
	body := rr.Body.String()
	if !strings.Contains(body, publicAPIKeyUsagePath) || !strings.Contains(body, "API Key 用量查询") || !strings.Contains(body, "近 7 天用量") {
		t.Fatalf("public usage page missing expected content")
	}
	if strings.Contains(body, "无需管理密钥") || strings.Contains(body, "统计口径") || strings.Contains(body, "备注：") {
		t.Fatalf("public usage page contains removed helper text")
	}
}
