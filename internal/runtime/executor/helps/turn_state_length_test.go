package helps

import (
	"context"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestUsageReporterTurnStateLength(t *testing.T) {
	var absent *UsageReporter
	absent.SetTurnStateLength(nil)
	r := NewUsageReporter(context.Background(), "codex", "model", nil)
	build := func() usage.Record { return r.buildRecordForModel("model", usage.Detail{}, false, usage.Failure{}) }
	if build().TurnStateLength != nil {
		t.Fatal("unset length must be nil")
	}
	length := 12
	r.SetTurnStateLength(&length)
	length = 99
	record := build()
	if record.TurnStateLength == nil || *record.TurnStateLength != 12 {
		t.Fatal("setter must copy the input")
	}
	*record.TurnStateLength = 100
	if *build().TurnStateLength != 12 {
		t.Fatal("record must not alias reporter state")
	}
	for _, value := range []int{0, 20, -1} {
		r.SetTurnStateLength(&value)
		got := build().TurnStateLength
		if value < 0 && got != nil || value >= 0 && (got == nil || *got != value) {
			t.Fatalf("length = %v, want %d (negative means nil)", got, value)
		}
	}
	r.SetTurnStateLength(nil)
	if build().TurnStateLength != nil {
		t.Fatal("nil must clear length")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			for value := 0; value < 100; value++ {
				r.SetTurnStateLength(&value)
				got := build().TurnStateLength
				if got == nil || *got < 0 {
					t.Error("concurrent snapshot missing length")
				}
			}
		})
	}
	wg.Wait()
}
