package usagecompat

import "testing"

func TestSnapshotWithOptionsIncludesCompleteAPITokenTotals(t *testing.T) {
	stats := &RequestStatistics{
		apis:           make(map[string]*apiStats),
		requestsByDay:  make(map[string]int64),
		requestsByHour: make(map[int]int64),
		tokensByDay:    make(map[string]int64),
		tokensByHour:   make(map[int]int64),
	}
	apiValue := &apiStats{Models: make(map[string]*modelStats)}
	stats.apis["sk-cache"] = apiValue

	stats.updateAPIStats(apiValue, "gpt-5.6", RequestDetail{Tokens: TokenStats{
		InputTokens:  100,
		OutputTokens: 20,
		CachedTokens: 80,
		TotalTokens:  120,
	}})
	stats.updateAPIStats(apiValue, "gpt-5.6", RequestDetail{Tokens: TokenStats{
		InputTokens:  200,
		OutputTokens: 30,
		CachedTokens: 70,
		TotalTokens:  230,
	}})

	snapshot := stats.SnapshotWithOptions(SnapshotOptions{DetailLimit: 1})
	apiSnapshot := snapshot.APIs["sk-cache"]
	if apiSnapshot.Tokens.InputTokens != 300 || apiSnapshot.Tokens.CachedTokens != 150 || apiSnapshot.Tokens.TotalTokens != 350 {
		t.Fatalf("api tokens = %#v, want input/cached/total 300/150/350", apiSnapshot.Tokens)
	}
	modelSnapshot := apiSnapshot.Models["gpt-5.6"]
	if modelSnapshot.Tokens.InputTokens != 300 || modelSnapshot.Tokens.CachedTokens != 150 || modelSnapshot.Tokens.TotalTokens != 350 {
		t.Fatalf("model tokens = %#v, want input/cached/total 300/150/350", modelSnapshot.Tokens)
	}
	if len(modelSnapshot.Details) != 1 || !modelSnapshot.DetailsTruncated {
		t.Fatalf("model details len/truncated = %d/%t, want 1/true", len(modelSnapshot.Details), modelSnapshot.DetailsTruncated)
	}
}
