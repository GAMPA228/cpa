package authheaders

import (
	"net/http"
	"strings"
	"testing"
)

func TestRulesValidation(t *testing.T) {
	for _, name := range []string{"Authorization", "Host", "Chatgpt-Account-Id", "Connection", "Sec-Websocket-Key", "Proxy-Authorization", "Session-Id", "X-Codex-Installation-Id", "X-Codex-Turn-Metadata"} {
		for _, op := range []string{"default", "override", "delete"} {
			if _, err := Decode([]Rule{{Name: name, Operation: op, Value: "secret"}}); err == nil {
				t.Fatalf("allowed protected %s (%s)", name, op)
			}
		}
	}
	for _, input := range []any{
		map[string]any{}, "bad", []Rule{{Name: "bad name", Operation: "override"}},
		[]Rule{{Name: "X-Test", Operation: "invalid"}},
		[]Rule{{Name: "X-Test", Operation: "override", Value: "secret\r\nInjected: yes"}},
		[]Rule{{Name: "X-Test", Operation: "override", Value: strings.Repeat("a", 8193)}},
		[]Rule{{Name: "X-Test", Operation: "override"}, {Name: "x-test", Operation: "delete"}},
		make([]Rule, 33),
	} {
		if _, err := Decode(input); err == nil {
			t.Fatal("accepted invalid rules")
		} else if strings.Contains(err.Error(), "secret") {
			t.Fatal("error leaks header value")
		}
	}
}

func TestRulesApply(t *testing.T) {
	rules, err := Decode([]Rule{
		{Name: "user-agent", Operation: "override", Value: "account-agent"},
		{Name: "Version", Operation: "default", Value: "fallback"},
		{Name: "X-New", Operation: "default", Value: "new"},
		{Name: "X-Remove", Operation: "delete"},
	})
	if err != nil {
		t.Fatal(err)
	}
	headers := http.Header{"User-Agent": {"global"}, "Version": {"existing"}, "X-Remove": {"old"}}
	Apply(headers, rules)
	if headers.Get("User-Agent") != "account-agent" || headers.Get("Version") != "existing" || headers.Get("X-New") != "new" || headers.Get("X-Remove") != "" {
		t.Fatalf("unexpected headers: %v", headers)
	}
	Apply(headers, nil)
	Apply(headers, []Rule{{Name: "User-Agent", Operation: "delete"}})
	if values, ok := headers["User-Agent"]; !ok || len(values) != 1 || values[0] != "" {
		t.Fatal("transport could reinsert default user agent")
	}
}
