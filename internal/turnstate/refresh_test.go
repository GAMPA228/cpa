package turnstate

import (
	"sync"
	"testing"
	"time"
)

func TestRefreshWindowSingleFlightAndRetry(t *testing.T) {
	m, clock, _ := newTestManager(t)
	configureTestManager(t, m, true, 292)
	old := testObservation(m, "a", "model", testEpoch, 1)
	m.persist(old)
	m.persist(testObservation(m, "b", "model", testEpoch, 1))
	m.persist(testObservation(m, "a", "other", testEpoch, 1))
	boundary := old.rule.ExpiresAt.Add(-RefreshBefore)
	clock.set(boundary.Add(-time.Nanosecond))
	if value, omit, refresh := m.PrepareRequest("a", "model"); value != old.rule.Value || omit || refresh != nil {
		t.Fatal("refresh started before its window")
	}
	clock.set(boundary)
	var wg sync.WaitGroup
	claims := make(chan *Refresh, 32)
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, omit, refresh := m.PrepareRequest("a", "model")
			if refresh != nil {
				if value != "" || !omit {
					t.Error("refresh must omit outgoing state")
				}
				claims <- refresh
			} else if value != old.rule.Value || omit {
				t.Error("concurrent normal request lost its still-valid state")
			}
		}()
	}
	wg.Wait()
	close(claims)
	if len(claims) != 1 {
		t.Fatalf("concurrent refreshes = %d, want 1", len(claims))
	}
	first := <-claims
	for _, pair := range []key{{"b", "model"}, {"a", "other"}} {
		_, omit, refresh := m.PrepareRequest(pair.auth, pair.model)
		if !omit || refresh == nil {
			t.Fatal("refresh reservation leaked across accounts or models")
		}
		refresh.Finish(false)
	}
	clock.set(boundary.Add(time.Minute))
	if _, _, refresh := m.PrepareRequest("a", "model"); refresh != nil {
		t.Fatal("an in-flight request was replaced by a timer")
	}
	first.Finish(true)
	clock.set(boundary.Add(time.Minute + RefreshRetryInterval - time.Nanosecond))
	if value, omit, refresh := m.PrepareRequest("a", "model"); value != old.rule.Value || omit || refresh != nil {
		t.Fatal("failed refresh did not keep old state during cooldown")
	}
	clock.set(boundary.Add(time.Minute + RefreshRetryInterval))
	_, omit, second := m.PrepareRequest("a", "model")
	if !omit || second == nil || second.ID() == first.ID() {
		t.Fatal("refresh did not retry with a fresh connection identity")
	}
	first.Finish(false)
	if _, _, refresh := m.PrepareRequest("a", "model"); refresh != nil {
		t.Fatal("late completion released a different refresh")
	}
	second.Finish(false)
	_, _, replay := m.PrepareRequest("a", "model")
	if replay == nil {
		t.Fatal("replay-required request introduced a cooldown without contacting upstream")
	}
	replay.Finish(true)
}

func TestRefreshRequiresDurableNewValue(t *testing.T) {
	m, clock, _ := newTestManager(t)
	configureTestManager(t, m, true, 292)
	old := testObservation(m, "a", "model", testEpoch, 1)
	m.persist(old)
	clock.set(testEpoch.Add(56 * time.Minute))
	_, _, refresh := m.PrepareRequest("a", "model")
	if refresh == nil {
		t.Fatal("missing refresh")
	}
	if _, err := m.store.db.Exec("CREATE TRIGGER reject_refresh BEFORE INSERT ON rules BEGIN SELECT RAISE(FAIL, 'test failure'); END"); err != nil {
		t.Fatal(err)
	}
	newRule := testObservation(m, "a", "model", clock.now(), 1)
	m.persist(newRule)
	refresh.Finish(true)
	if value, _, _ := m.PrepareRequest("a", "model"); value != old.rule.Value {
		t.Fatal("failed persistence replaced the active rule")
	}
	if _, err := m.store.db.Exec("DROP TRIGGER reject_refresh"); err != nil {
		t.Fatal(err)
	}
	clock.set(clock.now().Add(RefreshRetryInterval))
	_, _, refresh = m.PrepareRequest("a", "model")
	if refresh == nil {
		t.Fatal("failed persistence prevented retry")
	}
	m.persist(old)
	requireRule(t, m, old.rule)
	m.persist(newRule)
	refresh.Finish(true)
	if value, omit, next := m.PrepareRequest("a", "model"); value != newRule.rule.Value || omit || next != nil {
		t.Fatal("durable refreshed value was not applied")
	}
	requireRule(t, m, newRule.rule)
}

func TestRefreshExpiryThresholdAndDisabled(t *testing.T) {
	for _, scenario := range []string{"expired", "oversized"} {
		t.Run(scenario, func(t *testing.T) {
			m, clock, _ := newTestManager(t)
			configureTestManager(t, m, true, 292)
			m.persist(testObservation(m, "a", "model", testEpoch, 10))
			if scenario == "expired" {
				clock.set(testEpoch.Add(time.Hour))
			} else {
				configureTestManager(t, m, true, 100)
			}
			value, omit, refresh := m.PrepareRequest("a", "model")
			if value != "" || !omit || refresh == nil {
				t.Fatal("unusable automatic state did not trigger refresh")
			}
			for _, finish := range []bool{false, true} {
				if finish {
					refresh.Finish(true)
				}
				if value, omit, next := m.PrepareRequest("a", "model"); value != "" || !omit || next != nil {
					t.Fatal("unusable state or client passthrough restored during refresh/cooldown")
				}
			}
			configureTestManager(t, m, false, 292)
			if value, omit, next := m.PrepareRequest("a", "model"); value != "" || omit || next != nil {
				t.Fatal("disabled automation changed outgoing headers")
			}
			configureTestManager(t, m, true, 292)
			if value, omit, next := m.PrepareRequest("unknown", "model"); value != "" || omit || next != nil {
				t.Fatal("unknown account was automatically enrolled for probing")
			}
		})
	}
}

func TestRefreshRestartDiscardsReservations(t *testing.T) {
	m, clock, path := newTestManager(t)
	configureTestManager(t, m, true, 292)
	old := testObservation(m, "a", "model", testEpoch, 1)
	m.persist(old)
	clock.set(testEpoch.Add(56 * time.Minute))
	_, _, first := m.PrepareRequest("a", "model")
	m.Close()
	if value, omit, refresh := m.PrepareRequest("a", "model"); value != "" || omit || refresh != nil {
		t.Fatal("closed store still refreshed state")
	}
	if err := m.Open(path); err != nil {
		t.Fatal(err)
	}
	_, _, second := m.PrepareRequest("a", "model")
	if first == nil || second == nil || first.ID() == second.ID() {
		t.Fatal("restart retained an in-flight reservation")
	}
	first.Finish(true)
	if _, _, next := m.PrepareRequest("a", "model"); next != nil {
		t.Fatal("old reservation released the restarted request")
	}
	second.Finish(true)
	requireRule(t, m, old.rule)
}
