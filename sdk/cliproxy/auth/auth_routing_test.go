package auth

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestSchedulerRestrictsSelectionToAllowedAuthIDs(t *testing.T) {
	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "codex-a", Provider: "codex"},
		&Auth{ID: "codex-b", Provider: "codex"},
	)
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.AllowedCodexAuthIDsMetadataKey: []string{"codex-b"},
	}}

	for attempt := 0; attempt < 3; attempt++ {
		selected, errPick := scheduler.pickSingle(context.Background(), "codex", "", opts, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() error = %v", errPick)
		}
		if selected == nil || selected.ID != "codex-b" {
			t.Fatalf("pickSingle() = %#v, want codex-b", selected)
		}
	}
}

func TestSchedulerFailsClosedWhenAssignedAuthIsMissing(t *testing.T) {
	scheduler := newSchedulerForTest(&RoundRobinSelector{}, &Auth{ID: "codex-a", Provider: "codex"})
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.AllowedCodexAuthIDsMetadataKey: []string{"missing"},
	}}

	if selected, errPick := scheduler.pickSingle(context.Background(), "codex", "", opts, nil); errPick == nil || selected != nil {
		t.Fatalf("pickSingle() = %#v, %v, want fail-closed error", selected, errPick)
	}
}

func TestAuthRoutingMetadataAbsentPreservesUnrestrictedSelection(t *testing.T) {
	eligibility := authSelectionEligibilityForRequest(context.Background(), cliproxyexecutor.Options{})
	if !eligibility.allows(&Auth{ID: "codex-a", Provider: "codex"}) {
		t.Fatal("legacy request without auth routing metadata was restricted")
	}
}

func TestHomeSelectionFailsClosedForAuthRoutingRestriction(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.AllowedCodexAuthIDsMetadataKey: []string{"codex-a"},
	}}

	_, errSelection := manager.pickHomeDispatchSelection(context.Background(), "gpt-5.6", opts)
	if errSelection == nil {
		t.Fatal("pickHomeDispatchSelection() error = nil")
	}
	authErr, ok := errSelection.(*Error)
	if !ok || authErr.Code != "auth_routing_unsupported" {
		t.Fatalf("pickHomeDispatchSelection() error = %#v", errSelection)
	}
}
