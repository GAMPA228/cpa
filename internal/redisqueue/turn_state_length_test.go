package redisqueue

import (
	"context"
	"encoding/json"
	"testing"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestUsageQueueTurnStateLength(t *testing.T) {
	zero, positive, negative := 0, 123, -1
	for _, length := range []*int{nil, &zero, &positive, &negative} {
		withEnabledQueue(t, func() {
			(&usageQueuePlugin{}).HandleUsage(context.Background(), coreusage.Record{Model: "model", TurnStateLength: length})
			payload := popSinglePayload(t)
			raw, exists := payload["turn_state_length"]
			var got any
			if exists {
				if err := json.Unmarshal(raw, &got); err != nil {
					t.Fatal(err)
				}
			}
			var want any
			if length != nil && *length >= 0 {
				want = float64(*length)
			}
			if !exists || got != want {
				t.Fatalf("turn_state_length = %v, present %v, want %v", got, exists, want)
			}
		})
	}
}
