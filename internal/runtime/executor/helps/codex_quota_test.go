package helps

import "testing"

func TestParseCodexQuotaEventHeadersCapturesBaseWindows(t *testing.T) {
	payload := []byte(`{
		"type":"codex.rate_limits",
		"plan_type":"pro",
		"rate_limits":{
			"primary":{"used_percent":12.5,"window_minutes":300,"reset_after_seconds":120},
			"secondary":{"used_percent":30,"window_minutes":10080,"reset_at":1900000000}
		}
	}`)
	headers := ParseCodexQuotaEventHeaders(payload)
	if got := headers.Get("X-Codex-Primary-Used-Percent"); got != "12.5" {
		t.Fatalf("primary used percent = %q, want 12.5", got)
	}
	if got := headers.Get("X-Codex-Primary-Reset-After-Seconds"); got != "120" {
		t.Fatalf("primary reset after = %q, want 120", got)
	}
	if got := headers.Get("X-Codex-Secondary-Reset-At"); got != "1900000000" {
		t.Fatalf("secondary reset at = %q, want 1900000000", got)
	}
	if got := headers.Get("X-Codex-Plan-Type"); got != "pro" {
		t.Fatalf("plan type = %q, want pro", got)
	}
}

func TestParseCodexQuotaEventHeadersIgnoresOrdinaryFrames(t *testing.T) {
	if headers := ParseCodexQuotaEventHeaders([]byte(`{"type":"response.output_text.delta"}`)); headers != nil {
		t.Fatalf("ordinary frame headers = %#v, want nil", headers)
	}
}
