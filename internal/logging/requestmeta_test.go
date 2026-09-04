package logging

import (
	"context"
	"net/http"
	"testing"
)

func TestMergeResponseHeadersPreservesExistingValues(t *testing.T) {
	ctx := WithResponseHeadersHolder(context.Background())
	SetResponseHeaders(ctx, http.Header{"X-Upstream-Request-Id": {"request-1"}})
	MergeResponseHeaders(ctx, http.Header{"X-Codex-Primary-Used-Percent": {"25"}})

	headers := GetResponseHeaders(ctx)
	if got := headers.Get("X-Upstream-Request-Id"); got != "request-1" {
		t.Fatalf("request id = %q, want request-1", got)
	}
	if got := headers.Get("X-Codex-Primary-Used-Percent"); got != "25" {
		t.Fatalf("quota percent = %q, want 25", got)
	}
}
