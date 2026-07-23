package usagecompat

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
)

func TestModuleRegistersUsageCompatibilityRoutes(t *testing.T) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	prevEnabled := redisqueue.UsageStatisticsEnabled()
	redisqueue.SetUsageStatisticsEnabled(false)
	defer redisqueue.SetUsageStatisticsEnabled(prevEnabled)

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 8080\nusage-statistics-enabled: false\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg := &config.Config{
		Port:                   8080,
		UsageStatisticsEnabled: false,
	}
	stats := newTestRequestStatistics(t)
	controller := NewFileBackedConfigController(cfg, configPath)
	module := New(NewHandler(stats, controller))

	router := gin.New()
	if err := module.Register(router); err != nil {
		t.Fatalf("register module: %v", err)
	}

	usageResponse := performJSONRequest(t, router, http.MethodGet, "/v0/management/usage", nil)
	if got := int64(getJSONNumber(t, usageResponse["failed_requests"])); got != 0 {
		t.Fatalf("failed_requests = %d, want 0", got)
	}

	importPayload := map[string]any{
		"version": 1,
		"usage": map[string]any{
			"apis": map[string]any{
				"demo-key": map[string]any{
					"models": map[string]any{
						"gpt-4o": map[string]any{
							"details": []map[string]any{
								{
									"timestamp":  time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
									"latency_ms": 12,
									"source":     "test-source",
									"auth_index": "0",
									"tokens": map[string]any{
										"input_tokens":  1,
										"output_tokens": 2,
										"total_tokens":  3,
									},
									"failed": false,
								},
							},
						},
					},
				},
			},
		},
	}

	importResponse := performJSONRequest(t, router, http.MethodPost, "/v0/management/usage/import", importPayload)
	if got := int64(getJSONNumber(t, importResponse["added"])); got != 1 {
		t.Fatalf("added = %d, want 1", got)
	}
	if got := int64(getJSONNumber(t, importResponse["total_requests"])); got != 1 {
		t.Fatalf("total_requests = %d, want 1", got)
	}

	enabledResponse := performJSONRequest(t, router, http.MethodGet, "/v0/management/usage-statistics-enabled", nil)
	if got, ok := enabledResponse["usage-statistics-enabled"].(bool); !ok || got {
		t.Fatalf("usage-statistics-enabled = %#v, want false", enabledResponse["usage-statistics-enabled"])
	}

	updateResponse := performJSONRequest(t, router, http.MethodPut, "/v0/management/usage-statistics-enabled", map[string]any{"value": true})
	if status, ok := updateResponse["status"].(string); !ok || status != "ok" {
		t.Fatalf("status = %#v, want ok", updateResponse["status"])
	}
	if !cfg.UsageStatisticsEnabled {
		t.Fatal("cfg.UsageStatisticsEnabled = false, want true")
	}
	if !redisqueue.UsageStatisticsEnabled() {
		t.Fatal("redisqueue.UsageStatisticsEnabled() = false, want true")
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), "usage-statistics-enabled: true") {
		t.Fatalf("persisted config missing updated flag: %s", string(data))
	}
}

func TestUsageDashboardLimitsDetailsButExportKeepsAll(t *testing.T) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	stats := newTestRequestStatistics(t)
	module := New(NewHandler(stats, nil))

	router := gin.New()
	if err := module.Register(router); err != nil {
		t.Fatalf("register module: %v", err)
	}

	details := make([]map[string]any, 0, 3)
	for i := 0; i < 3; i++ {
		source := "test-source"
		authIndex := "0"
		clientIP := "10.0.0.1"
		if i == 1 {
			source = "other-source"
			authIndex = "1"
			clientIP = "10.0.0.2"
		}
		reasoningEffort := "high"
		if i == 1 {
			reasoningEffort = "xhigh"
		}
		details = append(details, map[string]any{
			"timestamp":        time.Date(2026, 5, 8, i, 0, 0, 0, time.UTC).Format(time.RFC3339),
			"latency_ms":       10 + i,
			"client_ip":        clientIP,
			"source":           source,
			"auth_index":       authIndex,
			"reasoning_effort": reasoningEffort,
			"tokens": map[string]any{
				"input_tokens":  1,
				"output_tokens": 2,
				"total_tokens":  3,
			},
			"failed": false,
		})
	}

	importPayload := map[string]any{
		"version": 1,
		"usage": map[string]any{
			"apis": map[string]any{
				"demo-key": map[string]any{
					"models": map[string]any{
						"gpt-4o": map[string]any{
							"details": details,
						},
					},
				},
			},
		},
	}
	performJSONRequest(t, router, http.MethodPost, "/v0/management/usage/import", importPayload)

	usageResponse := performJSONRequest(t, router, http.MethodGet, "/v0/management/usage?detail_limit=2", nil)
	model := nestedModelSnapshot(t, usageResponse)
	limitedDetails, ok := model["details"].([]any)
	if !ok {
		t.Fatalf("dashboard details = %#v, want array", model["details"])
	}
	if got := len(limitedDetails); got != 2 {
		t.Fatalf("dashboard details len = %d, want 2", got)
	}
	if got, ok := model["details_truncated"].(bool); !ok || !got {
		t.Fatalf("details_truncated = %#v, want true", model["details_truncated"])
	}

	exportResponse := performJSONRequest(t, router, http.MethodGet, "/v0/management/usage/export", nil)
	exportUsage, ok := exportResponse["usage"].(map[string]any)
	if !ok {
		t.Fatalf("export usage = %#v, want object", exportResponse["usage"])
	}
	exportModel := nestedModelSnapshot(t, map[string]any{"usage": exportUsage})
	exportDetails, ok := exportModel["details"].([]any)
	if !ok {
		t.Fatalf("export details = %#v, want array", exportModel["details"])
	}
	if got := len(exportDetails); got != 3 {
		t.Fatalf("export details len = %d, want 3", got)
	}

	detailPage := performJSONRequest(t, router, http.MethodGet, "/v0/management/usage/details?page=2&page_size=2", nil)
	items, ok := detailPage["items"].([]any)
	if !ok {
		t.Fatalf("detail page items = %#v, want array", detailPage["items"])
	}
	if got := len(items); got != 1 {
		t.Fatalf("detail page items len = %d, want 1", got)
	}
	if got := int64(getJSONNumber(t, detailPage["total"])); got != 3 {
		t.Fatalf("detail page total = %d, want 3", got)
	}
	if got, ok := detailPage["has_more"].(bool); !ok || got {
		t.Fatalf("detail page has_more = %#v, want false", detailPage["has_more"])
	}

	filteredDetailPage := performJSONRequest(t, router, http.MethodGet, "/v0/management/usage/details?page_size=10&source=other-source&auth_index=1&search=10.0.0.2", nil)
	filteredItems, ok := filteredDetailPage["items"].([]any)
	if !ok {
		t.Fatalf("filtered detail page items = %#v, want array", filteredDetailPage["items"])
	}
	if got := len(filteredItems); got != 1 {
		t.Fatalf("filtered detail page items len = %d, want 1", got)
	}
	if got := int64(getJSONNumber(t, filteredDetailPage["total"])); got != 1 {
		t.Fatalf("filtered detail page total = %d, want 1", got)
	}
	filteredItem, ok := filteredItems[0].(map[string]any)
	if !ok {
		t.Fatalf("filtered detail item = %#v, want object", filteredItems[0])
	}
	if got, ok := filteredItem["reasoning_effort"].(string); !ok || got != "xhigh" {
		t.Fatalf("filtered detail reasoning_effort = %#v, want xhigh", filteredItem["reasoning_effort"])
	}
	if got, ok := filteredItem["client_ip"].(string); !ok || got != "10.0.0.2" {
		t.Fatalf("filtered detail client_ip = %#v, want 10.0.0.2", filteredItem["client_ip"])
	}

	aggregate := performJSONRequest(t, router, http.MethodGet, "/v0/management/usage/aggregate?range=all", nil)
	if got := int64(getJSONNumber(t, aggregate["total_requests"])); got != 3 {
		t.Fatalf("aggregate total_requests = %d, want 3", got)
	}
	if got := int64(getJSONNumber(t, aggregate["total_tokens"])); got != 9 {
		t.Fatalf("aggregate total_tokens = %d, want 9", got)
	}
	tokens, ok := aggregate["tokens"].(map[string]any)
	if !ok {
		t.Fatalf("aggregate tokens = %#v, want object", aggregate["tokens"])
	}
	if got := int64(getJSONNumber(t, tokens["input_tokens"])); got != 3 {
		t.Fatalf("aggregate input_tokens = %d, want 3", got)
	}
	aggregateAPIs, ok := aggregate["apis"].(map[string]any)
	if !ok {
		t.Fatalf("aggregate apis = %#v, want object", aggregate["apis"])
	}
	aggregateAPI, ok := aggregateAPIs["demo-key"].(map[string]any)
	if !ok {
		t.Fatalf("aggregate api = %#v, want object", aggregateAPIs["demo-key"])
	}
	if got := int64(getJSONNumber(t, aggregateAPI["total_requests"])); got != 3 {
		t.Fatalf("aggregate api total_requests = %d, want 3", got)
	}
	aggregateModels, ok := aggregate["models"].(map[string]any)
	if !ok {
		t.Fatalf("aggregate models = %#v, want object", aggregate["models"])
	}
	aggregateModel, ok := aggregateModels["gpt-4o"].(map[string]any)
	if !ok {
		t.Fatalf("aggregate model = %#v, want object", aggregateModels["gpt-4o"])
	}
	if got := int64(getJSONNumber(t, aggregateModel["total_tokens"])); got != 9 {
		t.Fatalf("aggregate model total_tokens = %d, want 9", got)
	}
	hourly, ok := aggregate["hourly"].([]any)
	if !ok || len(hourly) == 0 {
		t.Fatalf("aggregate hourly = %#v, want non-empty array", aggregate["hourly"])
	}
	firstHourly, ok := hourly[0].(map[string]any)
	if !ok {
		t.Fatalf("aggregate hourly[0] = %#v, want object", hourly[0])
	}
	if got, ok := firstHourly["api"].(string); !ok || got != "demo-key" {
		t.Fatalf("aggregate hourly api = %#v, want demo-key", firstHourly["api"])
	}
	daily, ok := aggregate["daily"].([]any)
	if !ok || len(daily) == 0 {
		t.Fatalf("aggregate daily = %#v, want non-empty array", aggregate["daily"])
	}
}

func newTestRequestStatistics(t *testing.T) *RequestStatistics {
	t.Helper()
	t.Setenv(usageSQLitePathEnv, filepath.Join(t.TempDir(), "usage.sqlite3"))
	stats := NewRequestStatistics()
	t.Cleanup(func() {
		if stats != nil && stats.detailStore != nil && stats.detailStore.db != nil {
			_ = stats.detailStore.db.Close()
		}
	})
	return stats
}

func performJSONRequest(t *testing.T, handler http.Handler, method, path string, body any) map[string]any {
	t.Helper()

	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(data)
	}

	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("%s %s status = %d, body = %s", method, path, recorder.Code, recorder.Body.String())
	}

	var result map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return result
}

func getJSONNumber(t *testing.T, value any) float64 {
	t.Helper()
	number, ok := value.(float64)
	if !ok {
		t.Fatalf("value %#v is not a JSON number", value)
	}
	return number
}

func nestedModelSnapshot(t *testing.T, response map[string]any) map[string]any {
	t.Helper()

	usage, ok := response["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage = %#v, want object", response["usage"])
	}
	apis, ok := usage["apis"].(map[string]any)
	if !ok {
		t.Fatalf("apis = %#v, want object", usage["apis"])
	}
	api, ok := apis["demo-key"].(map[string]any)
	if !ok {
		t.Fatalf("api = %#v, want object", apis["demo-key"])
	}
	models, ok := api["models"].(map[string]any)
	if !ok {
		t.Fatalf("models = %#v, want object", api["models"])
	}
	model, ok := models["gpt-4o"].(map[string]any)
	if !ok {
		t.Fatalf("model = %#v, want object", models["gpt-4o"])
	}
	return model
}
