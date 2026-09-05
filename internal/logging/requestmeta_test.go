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

func TestWithFreshResponseHeadersHolderIsolatesAttempts(t *testing.T) {
	requestCtx := WithResponseHeadersHolder(context.Background())
	SetResponseHeaders(requestCtx, http.Header{"X-Upstream-Attempt": []string{"request"}})

	firstAttemptCtx := WithFreshResponseHeadersHolder(requestCtx)
	if headers := GetResponseHeaders(firstAttemptCtx); len(headers) != 0 {
		t.Fatalf("fresh attempt inherited request headers: %#v", headers)
	}
	SetResponseHeaders(firstAttemptCtx, http.Header{"X-Upstream-Attempt": []string{"first"}})

	secondAttemptCtx := WithFreshResponseHeadersHolder(firstAttemptCtx)
	if headers := GetResponseHeaders(secondAttemptCtx); len(headers) != 0 {
		t.Fatalf("second attempt inherited first attempt headers: %#v", headers)
	}
	SetResponseHeaders(secondAttemptCtx, http.Header{"X-Upstream-Attempt": []string{"second"}})

	if got := GetResponseHeaders(requestCtx).Get("X-Upstream-Attempt"); got != "request" {
		t.Fatalf("request holder = %q, want request", got)
	}
	if got := GetResponseHeaders(firstAttemptCtx).Get("X-Upstream-Attempt"); got != "first" {
		t.Fatalf("first attempt holder = %q, want first", got)
	}
	if got := GetResponseHeaders(secondAttemptCtx).Get("X-Upstream-Attempt"); got != "second" {
		t.Fatalf("second attempt holder = %q, want second", got)
	}
}
