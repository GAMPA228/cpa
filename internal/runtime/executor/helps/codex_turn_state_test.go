package helps

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/turnstate"
)

func TestCodexTurnStateEventHeaders(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		want    http.Header
	}{
		{"string", `{"type":"codex.response.metadata","headers":{"x-codex-turn-state":"token","Set-Cookie":"secret"}}`, http.Header{turnstate.Header: {"token"}}},
		{"array", `{"type":"codex.response.metadata","headers":{"X-Codex-Turn-State":["one",42,"two"]}}`, http.Header{turnstate.Header: {"one", "two"}}},
		{"nonstring", `{"type":"codex.response.metadata","headers":{"X-Codex-Turn-State":42}}`, http.Header{}},
		{"ordinary event", `{"type":"response.output_text.delta","headers":{"X-Codex-Turn-State":"not-a-header"}}`, nil},
		{"malformed", `not-json`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CodexTurnStateEventHeaders([]byte(tc.payload)); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("headers = %v, want %v", got, tc.want)
			}
		})
	}
}
