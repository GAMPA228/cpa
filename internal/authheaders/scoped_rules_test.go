package authheaders

import (
	"encoding/json"
	"testing"
	"time"
)

func TestScopedRulesAndExpiry(t *testing.T) {
	now := time.Date(2026, 9, 17, 14, 0, 0, 0, time.UTC)
	expiry := now.Add(10 * time.Minute)
	rules, err := Decode([]Rule{
		{Name: "Version", Operation: "override", Value: "account"},
		{Name: "Version", Operation: "override", Value: "model-a", Models: []string{"gpt-5.4"}, DurationMinutes: 10, ExpiresAt: &expiry},
		{Name: "Version", Operation: "delete", Models: []string{"gpt-5.5"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		model     string
		at        time.Time
		value, op string
	}{
		{"gpt-5.4", now, "model-a", "override"}, {"gpt-5.4", expiry.Add(-time.Nanosecond), "model-a", "override"},
		{"gpt-5.4", expiry, "account", "override"}, {"gpt-5.5", now, "", "delete"}, {"other", now, "account", "override"},
	} {
		got := Select(rules, tc.model, tc.at)
		if len(got) != 1 || got[0].Value != tc.value || got[0].Operation != tc.op {
			t.Fatalf("unexpected selection: %+v", got)
		}
	}
	if Signature(Select(rules, "gpt-5.4", now)) == Signature(Select(rules, "gpt-5.4", expiry)) {
		t.Fatal("expiry did not change connection key")
	}
	data, _ := json.Marshal(rules)
	var raw any
	_ = json.Unmarshal(data, &raw)
	restored, err := Decode(raw)
	if err != nil || !restored[1].ExpiresAt.Equal(expiry) || restored[0].ID != rules[0].ID {
		t.Fatal("restart changed expiry or IDs")
	}
	if _, err := Decode(append(rules, Rule{Name: "version", Operation: "override", Models: []string{"gpt-5.4", "gpt-5.6"}})); err == nil {
		t.Fatal("allowed overlapping scopes")
	}
	for _, minutes := range []int{-1, 1, 9, 11, 61, 100} {
		if ValidDuration(minutes) {
			t.Fatal("invalid duration accepted")
		}
	}
	for _, minutes := range []int{0, 10, 20, 30, 40, 50, 60} {
		if !ValidDuration(minutes) {
			t.Fatal("valid duration rejected")
		}
	}
}
