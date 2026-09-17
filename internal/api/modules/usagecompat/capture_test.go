package usagecompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/diagnostics"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestCaptureRoutesAndUsageAssociation(t *testing.T) {
	stats := newTestRequestStatistics(t)
	old := diagnostics.Default
	diagnostics.Default = diagnostics.NewManager()
	t.Cleanup(func() { diagnostics.Default.Close(); diagnostics.Default = old })
	previous := redisqueue.UsageStatisticsEnabled()
	redisqueue.SetUsageStatisticsEnabled(true)
	t.Cleanup(func() { redisqueue.SetUsageStatisticsEnabled(previous) })
	router := gin.New()
	module := New(NewHandler(stats, nil), WithMiddleware(func(c *gin.Context) {
		if c.GetHeader("Authorization") != "Bearer admin-test" {
			c.AbortWithStatus(401)
		}
	}))
	if err := module.Register(router); err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string, authorized bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/v0/management"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if authorized {
			req.Header.Set("Authorization", "Bearer admin-test")
		}
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}
	for _, method := range []string{"GET", "PUT"} {
		if r := call(method, "/usage/capture", `{"enabled":true}`, false); r.Code != 401 {
			t.Fatal("unauthenticated capture access")
		}
	}
	for _, method := range []string{"GET", "DELETE"} {
		if r := call(method, "/usage/captures/"+uuid.NewString(), "", false); r.Code != 401 {
			t.Fatal("unauthenticated payload access")
		}
	}
	if r := call("PUT", "/usage/capture", `{"enabled":true}`, true); r.Code != 200 || r.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("enable: %d %s", r.Code, r.Body.String())
	}
	ctx := diagnostics.Default.Context(context.Background())
	a := diagnostics.Begin(ctx, diagnostics.Metadata{URL: "https://example.com/responses"}, []byte("request"), false)
	a.Headers(429, http.Header{"Retry-After": {"1"}}, false)
	a.Append([]byte(`{"error":"test"}`))
	a.Finish("", false)
	stats.Record(ctx, coreusage.Record{APIKey: "key", Model: "model", RequestedAt: time.Now(), CaptureID: diagnostics.ID(ctx), Detail: coreusage.Detail{TotalTokens: 1}})
	page := stats.DetailsPage(DetailPageQuery{})
	if len(page.Items) != 1 || page.Items[0].CaptureID != diagnostics.ID(ctx) {
		t.Fatalf("association missing: %+v", page)
	}
	if stats.Snapshot().APIs["key"].Models["model"].Details[0].CaptureID != diagnostics.ID(ctx) {
		t.Fatal("snapshot association missing")
	}
	if r := call("GET", "/usage/captures/"+diagnostics.ID(ctx), "", true); r.Code != 200 || !strings.Contains(r.Body.String(), "Retry-After") {
		t.Fatalf("read: %s", r.Body.String())
	}
	if r := call("GET", "/usage/captures/not-a-uuid", "", true); r.Code != 400 {
		t.Fatal("invalid ID accepted")
	}
	redisqueue.SetUsageStatisticsEnabled(false)
	if r := call("PUT", "/usage/capture", `{"enabled":true}`, true); r.Code != 409 {
		t.Fatal("capture without statistics accepted")
	}
}
