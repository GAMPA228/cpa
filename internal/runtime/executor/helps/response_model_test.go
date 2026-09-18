package helps

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestResponseModelParsing(t *testing.T) {
	for _, tc := range []struct{ payload, want string }{
		{`{"model":"upstream"}`, "upstream"},
		{`{"response":{"model":"actual"},"model":"wrapper"}`, "actual"},
		{`data: {"response":{"model":"actual"}}`, "actual"},
		{`{"model":123}`, ""},
		{`{"model":" "}`, ""},
		{`{"input":{"model":"not-a-response"}}`, ""},
		{`{"model":"truncated"`, ""},
	} {
		if got := responseModelFromPayload([]byte(tc.payload)); got != tc.want {
			t.Fatalf("payload %s: got %q want %q", tc.payload, got, tc.want)
		}
	}
	if got := ParseOpenAIUsage([]byte(`{"model":"actual","usage":{"total_tokens":7}}`)); got.ResponseModel != "actual" || got.TotalTokens != 7 {
		t.Fatalf("unexpected JSON detail: %+v", got)
	}
	if got, ok := ParseCodexUsage([]byte(`{"response":{"model":"actual","usage":{"total_tokens":7}}}`)); !ok || got.ResponseModel != "actual" || got.TotalTokens != 7 {
		t.Fatalf("unexpected Codex detail: %+v", got)
	}
}

func TestResponseModelRetainedOnFailureAndAfterTTFT(t *testing.T) {
	r := NewUsageReporter(context.Background(), "codex", "requested", nil)
	r.StartResponseTTFT()
	ObserveResponsesTokenEvent(r, []byte(`{"type":"response.created","response":{"model":"early"}}`))
	ObserveResponsesTokenEvent(r, []byte(`{"type":"response.output_text.delta","delta":"hello"}`))
	if !r.IsTTFTSet() {
		t.Fatal("token event did not set TTFT")
	}
	if got := r.buildRecord(usage.Detail{}, true); got.ResponseModel != "early" || got.Detail.ResponseModel != "early" {
		t.Fatalf("lost early model on failure: %+v", got)
	}
	ObserveResponsesTokenEvent(r, []byte(`{"type":"response.completed","response":{"model":"final"}}`))
	if got := r.buildRecord(usage.Detail{}, false); got.ResponseModel != "final" {
		t.Fatalf("final model = %q", got.ResponseModel)
	}
	other := NewUsageReporter(context.Background(), "codex", "requested", nil)
	if got := other.buildRecord(usage.Detail{}, true); got.ResponseModel != "" {
		t.Fatal("request model used as response model")
	}
}

func TestResponseModelStreamBufferPreservesUsage(t *testing.T) {
	var b StreamUsageBuffer
	b.ObserveOpenAIStream([]byte(`data: {"model":"early"}`))
	b.ObserveOpenAIStream([]byte(`data: {"usage":{"total_tokens":12}}`))
	b.ObserveOpenAIStream([]byte(`data: {"model":"final"}`))
	got, ok := b.Detail()
	if !ok || got.ResponseModel != "final" || got.TotalTokens != 12 {
		t.Fatalf("unexpected detail: %+v", got)
	}
	b.Observe(usage.Detail{ResponseServiceTier: "default"}, true)
	got, _ = b.Detail()
	if got.ResponseModel != "final" || got.TotalTokens != 12 {
		t.Fatalf("metadata erased usage: %+v", got)
	}
}

func TestResponseModelPluginRootUsageNotShadowed(t *testing.T) {
	payload := []byte(`{"model":"actual","usage":{"input_tokens":5,"output_tokens":7,"total_tokens":12}}`)
	for _, protocol := range []string{"codex", "openai-response", "openai"} {
		detail := ParsePluginExecutorResponseUsage(protocol, payload)
		if detail.ResponseModel != "actual" || detail.TotalTokens != 12 {
			t.Fatalf("%s non-stream detail: %+v", protocol, detail)
		}
		var buffer StreamUsageBuffer
		ObservePluginExecutorStreamUsage(protocol, append([]byte("data: "), payload...), &buffer)
		detail, ok := buffer.Detail()
		if !ok || detail.ResponseModel != "actual" || detail.TotalTokens != 12 {
			t.Fatalf("%s stream detail: %+v", protocol, detail)
		}
	}
}
