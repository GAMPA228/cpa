package redisqueue

import (
	"context"
	"testing"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestUsageQueueResponseModel(t *testing.T) {
	cases := []struct{ name, record, detail, want string }{
		{"record wins", " upstream-version ", "ignored", "upstream-version"},
		{"detail fallback", " \t", " upstream-fallback ", "upstream-fallback"},
		{"no requested model fallback", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withEnabledQueue(t, func() {
				(&usageQueuePlugin{}).HandleUsage(context.Background(), coreusage.Record{
					Model: "requested-alias", ResponseModel: tc.record,
					Detail: coreusage.Detail{ResponseModel: tc.detail},
				})
				payload := popSinglePayload(t)
				requireStringField(t, payload, "model", "requested-alias")
				if tc.want == "" {
					requireMissingField(t, payload, "response_model")
				} else {
					requireStringField(t, payload, "response_model", tc.want)
				}
			})
		})
	}
}
