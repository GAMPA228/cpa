package turnstate

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testClock struct{ nanos atomic.Int64 }

func (c *testClock) set(now time.Time) { c.nanos.Store(now.UnixNano()) }
func (c *testClock) now() time.Time    { return time.Unix(0, c.nanos.Load()).UTC() }

func openTestManager(t *testing.T, path string, clock *testClock) *Manager {
	t.Helper()
	m := NewManager()
	m.now = clock.now
	if err := m.Open(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}

func newTestManager(t *testing.T) (*Manager, *testClock, string) {
	t.Helper()
	c := &testClock{}
	c.set(testEpoch)
	path := filepath.Join(t.TempDir(), "nested", "turnstate.db")
	return openTestManager(t, path, c), c, path
}

func configureTestManager(t *testing.T, m *Manager, enabled bool, max int) {
	t.Helper()
	if err := m.Configure(Settings{Enabled: enabled, MaxChars: max}); err != nil {
		t.Fatal(err)
	}
}

func testObservation(m *Manager, auth, model string, issued time.Time, blocks int) observation {
	m.mu.RLock()
	generation := m.generation
	m.mu.RUnlock()
	return observation{generation: generation, rule: Rule{
		AuthID: auth, Model: model, Value: testToken(issued, blocks),
		IssuedAt: issued, ExpiresAt: issued.Add(time.Hour),
	}}
}

func requireRule(t *testing.T, m *Manager, want Rule) {
	t.Helper()
	got, ok := m.Lookup(want.AuthID, want.Model)
	if !ok || got != want {
		t.Fatalf("Lookup(%q, %q) = %+v, %v; want %+v", want.AuthID, want.Model, got, ok, want)
	}
}

func TestConfigurableLifetimeUpdatesExistingRulesAndPersists(t *testing.T) {
	m, clock, path := newTestManager(t)
	configureTestManager(t, m, true, 292)
	m.persist(testObservation(m, "a", "model", testEpoch, 1))
	seconds := 240
	if err := m.PatchSettings(true, 292, nil, nil, &seconds); err != nil {
		t.Fatal(err)
	}
	if got, ok := m.Lookup("a", "model"); !ok || !got.ExpiresAt.Equal(testEpoch.Add(240*time.Second)) {
		t.Fatalf("updated expiry = %+v, %v", got, ok)
	}
	clock.set(testEpoch.Add(181 * time.Second))
	_, omit, refresh := m.PrepareRequest("a", "model")
	if !omit || refresh == nil {
		t.Fatal("short lifetime did not refresh before expiry")
	}
	refresh.Finish(false)
	m.Close()
	m = openTestManager(t, path, clock)
	if got := m.Status().LifetimeSeconds; got != seconds {
		t.Fatalf("persisted lifetime = %d", got)
	}
	clock.set(testEpoch.Add(241 * time.Second))
	if _, ok := m.Lookup("a", "model"); ok {
		t.Fatal("expired rule remained active")
	}
}

func TestObserveDrainDeduplicatesAndScopesRules(t *testing.T) {
	m, clock, path := newTestManager(t)
	if got := m.Status(); !reflect.DeepEqual(got.Settings, Settings{MaxChars: 292, LifetimeSeconds: 3600, AccountScope: "all", AuthIDs: []string{}}) {
		t.Fatalf("persisted defaults = %+v", got)
	}
	m.Observe("disabled", "model", testToken(testEpoch, 1))
	configureTestManager(t, m, true, 292)
	for _, pair := range []key{{"a", "model-a"}, {"a", "model-b"}, {"b", "model-a"}} {
		for range 8 {
			m.Observe(pair.auth, pair.model, testToken(testEpoch, 1))
		}
	}
	for _, pair := range []key{{"", "model"}, {"a", ""}, {"a", strings.Repeat("m", 257)}} {
		m.Observe(pair.auth, pair.model, testToken(testEpoch, 1))
	}
	m.Observe("a", "malformed", "not-fernet")
	m.Observe("a", "oversized", testToken(testEpoch, 11))
	m.Observe("a", "expired", testToken(testEpoch.Add(-time.Hour), 1))
	m.Observe("a", "future", testToken(testEpoch.Add(61*time.Second), 1))
	m.Close() // Joining the worker proves every queued observation has finished.
	m = openTestManager(t, path, clock)
	for _, pair := range []key{{"a", "model-a"}, {"a", "model-b"}, {"b", "model-a"}} {
		requireRule(t, m, testObservation(m, pair.auth, pair.model, testEpoch, 1).rule)
	}
	if _, ok := m.Lookup("b", "model-b"); ok {
		t.Fatal("rule leaked between accounts")
	}
	var count int
	if err := m.store.db.QueryRow("SELECT COUNT(*) FROM rules").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("stored rules = %d, want 3", count)
	}
	if got := m.Rules("a"); len(got) != 2 || got[0].Model != "model-a" || got[1].Model != "model-b" {
		t.Fatalf("sorted account rules = %+v", got)
	}
}

func TestPersistRefreshWindowAndExpiry(t *testing.T) {
	m, clock, _ := newTestManager(t)
	configureTestManager(t, m, true, 292)
	initial := testObservation(m, "a", "model", testEpoch, 1)
	m.persist(initial)
	clock.set(testEpoch.Add(55*time.Minute - time.Nanosecond))
	newer := testObservation(m, "a", "model", testEpoch.Add(30*time.Minute), 1)
	m.persist(newer)
	requireRule(t, m, initial.rule)
	clock.set(testEpoch.Add(55 * time.Minute))
	m.persist(testObservation(m, "a", "model", testEpoch.Add(-time.Minute), 1))
	m.persist(testObservation(m, "a", "model", testEpoch, 2))
	requireRule(t, m, initial.rule)
	m.persist(newer)
	requireRule(t, m, newer.rule)
	clock.set(newer.rule.ExpiresAt.Add(-time.Nanosecond))
	requireRule(t, m, newer.rule)
	clock.set(newer.rule.ExpiresAt)
	if _, ok := m.Lookup("a", "model"); ok {
		t.Fatal("rule active at exact expiry")
	}
	if got := m.Rules("a"); len(got) != 1 || got[0] != newer.rule {
		t.Fatalf("expired rule not retained: %+v", got)
	}
}

func TestLowerThresholdInvalidatesLookupAndAllowsNewerReplacement(t *testing.T) {
	m, clock, _ := newTestManager(t)
	configureTestManager(t, m, true, 292)
	initial := testObservation(m, "a", "model", testEpoch, 10)
	if len(initial.rule.Value) != 292 {
		t.Fatal("fixture must exercise exact default threshold")
	}
	m.persist(initial)
	requireRule(t, m, initial.rule)
	configureTestManager(t, m, true, 100)
	if _, ok := m.Lookup("a", "model"); ok {
		t.Fatal("lower threshold did not invalidate Lookup")
	}
	clock.set(testEpoch.Add(time.Minute))
	m.persist(testObservation(m, "a", "model", testEpoch, 1))
	if _, ok := m.Lookup("a", "model"); ok {
		t.Fatal("equal timestamp replaced oversized rule")
	}
	replacement := testObservation(m, "a", "model", clock.now(), 1)
	m.persist(replacement)
	requireRule(t, m, replacement.rule)
}

func TestDisableCancelsQueuedGenerationEvenAfterReenable(t *testing.T) {
	m, _, _ := newTestManager(t)
	configureTestManager(t, m, true, 292)
	queued := testObservation(m, "a", "model", testEpoch, 1)
	configureTestManager(t, m, false, 292)
	m.persist(queued)
	if len(m.Rules("a")) != 0 {
		t.Fatal("disabled manager persisted queued observation")
	}
	configureTestManager(t, m, true, 292)
	m.persist(queued)
	if len(m.Rules("a")) != 0 {
		t.Fatal("reenable revived stale queued observation")
	}
	fresh := testObservation(m, "a", "model", testEpoch, 1)
	m.persist(fresh)
	requireRule(t, m, fresh.rule)
	configureTestManager(t, m, false, 292)
	if _, ok := m.Lookup("a", "model"); ok {
		t.Fatal("disabled manager exposed existing rule")
	}
}

func TestPersistenceRestartKeepsSettingsAndOriginalDeadlines(t *testing.T) {
	m, clock, path := newTestManager(t)
	configureTestManager(t, m, true, 200)
	clock.set(testEpoch.Add(20 * time.Minute))
	obs := testObservation(m, "a", "model", testEpoch, 1)
	m.Observe("a", "model", obs.rule.Value)
	m.Close()
	clock.set(testEpoch.Add(40 * time.Minute))
	m = openTestManager(t, path, clock)
	if got := m.Status().Settings; !reflect.DeepEqual(got, Settings{Enabled: true, MaxChars: 200, LifetimeSeconds: 3600, AccountScope: "all", AuthIDs: []string{}}) {
		t.Fatalf("restarted settings = %+v", got)
	}
	requireRule(t, m, obs.rule)
	configureTestManager(t, m, false, 150)
	m.Close()
	clock.set(testEpoch.Add(time.Hour))
	m = openTestManager(t, path, clock)
	if got := m.Status().Settings; !reflect.DeepEqual(got, Settings{MaxChars: 150, LifetimeSeconds: 3600, AccountScope: "all", AuthIDs: []string{}}) {
		t.Fatalf("disabled settings not persisted: %+v", got)
	}
	configureTestManager(t, m, true, 150)
	if _, ok := m.Lookup("a", "model"); ok {
		t.Fatal("restart extended deadline")
	}
	if got := m.Rules("a"); len(got) != 1 || got[0] != obs.rule {
		t.Fatalf("restart changed expired rule: %+v", got)
	}
}

func TestStorageFailureDoesNotActivateOrReplaceRule(t *testing.T) {
	m, clock, path := newTestManager(t)
	configureTestManager(t, m, true, 292)
	initial := testObservation(m, "a", "model", testEpoch, 1)
	m.persist(initial)
	// Reject writes without closing the database, so restart can verify durable state.
	if _, err := m.store.db.Exec("CREATE TRIGGER reject_rules BEFORE INSERT ON rules BEGIN SELECT RAISE(FAIL, 'test write failure'); END"); err != nil {
		t.Fatal(err)
	}
	clock.set(testEpoch.Add(55 * time.Minute))
	m.persist(testObservation(m, "a", "model", clock.now(), 1))
	m.persist(testObservation(m, "b", "model", clock.now(), 1))
	requireRule(t, m, initial.rule)
	if _, ok := m.Lookup("b", "model"); ok {
		t.Fatal("failed write activated rule")
	}
	if got := m.Status().StorageErrors; got != 2 {
		t.Fatalf("storage errors = %d, want 2", got)
	}
	if _, err := m.store.db.Exec("CREATE TRIGGER reject_settings BEFORE UPDATE ON settings BEGIN SELECT RAISE(FAIL, 'test settings failure'); END"); err != nil {
		t.Fatal(err)
	}
	before := m.Status().Settings
	if err := m.Configure(Settings{MaxChars: 100}); err == nil {
		t.Fatal("Configure succeeded despite storage failure")
	}
	if got := m.Status().Settings; !reflect.DeepEqual(got, before) {
		t.Fatalf("failed Configure changed settings: %+v", got)
	}
	m.Close()
	m = openTestManager(t, path, clock)
	requireRule(t, m, initial.rule)
	if len(m.Rules("b")) != 0 || !reflect.DeepEqual(m.Status().Settings, before) {
		t.Fatal("failed writes survived restart")
	}
}

func TestConfigureRejectsInvalidThresholds(t *testing.T) {
	m, _, _ := newTestManager(t)
	for _, max := range []int{-1, 0, MaxChars + 1} {
		if err := m.Configure(Settings{Enabled: true, MaxChars: max}); err == nil {
			t.Fatalf("accepted max_chars=%d", max)
		}
	}
	if got := m.Status().Settings; !reflect.DeepEqual(got, Settings{MaxChars: 292, LifetimeSeconds: 3600, AccountScope: "all", AuthIDs: []string{}}) {
		t.Fatalf("invalid settings activated: %+v", got)
	}
	for _, max := range []int{1, MaxChars} {
		configureTestManager(t, m, true, max)
	}
	m.Close()
	if err := m.Configure(Settings{MaxChars: 292}); err == nil {
		t.Fatal("Configure after Close succeeded")
	}
}

func TestConcurrentObservePersistLookupAndConfigure(t *testing.T) {
	m, clock, path := newTestManager(t)
	configureTestManager(t, m, true, 292)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			auth := fmt.Sprintf("account-%d", i)
			for range 16 {
				m.Observe(auth, "model", testToken(testEpoch, 1))
				m.persist(testObservation(m, auth, "model", testEpoch, 1))
				m.Lookup(auth, "model")
				m.Rules(auth)
				m.Status()
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for range 8 {
			if err := m.Configure(Settings{Enabled: true, MaxChars: 292}); err != nil {
				t.Errorf("concurrent Configure: %v", err)
			}
		}
	}()
	close(start)
	wg.Wait()
	// A final current-generation observation makes the expected result independent
	// of which concurrent observations were intentionally invalidated by Configure.
	for i := range 8 {
		m.persist(testObservation(m, fmt.Sprintf("account-%d", i), "model", testEpoch, 1))
	}
	m.Close()
	if got := m.Status().StorageErrors; got != 0 {
		t.Fatalf("concurrent storage errors = %d", got)
	}
	m = openTestManager(t, path, clock)
	for i := range 8 {
		requireRule(t, m, testObservation(m, fmt.Sprintf("account-%d", i), "model", testEpoch, 1).rule)
	}
}

func TestPersistReclaimsOldestExpiredRuleAtomically(t *testing.T) {
	for _, failInsert := range []bool{false, true} {
		name := "commit"
		if failInsert {
			name = "rollback"
		}
		t.Run(name, func(t *testing.T) {
			m, clock, path := newTestManager(t)
			configureTestManager(t, m, true, 292)
			other := testObservation(m, "other", "oldest-overall", testEpoch.Add(-time.Minute), 1)
			m.persist(other)
			oldest := testObservation(m, "a", "oldest", testEpoch, 1)
			m.persist(oldest)
			clock.set(testEpoch.Add(time.Minute))
			m.persist(testObservation(m, "a", "expires-exactly-now", clock.now(), 1))
			clock.set(testEpoch.Add(30 * time.Minute))
			for i := range 126 {
				m.persist(testObservation(m, "a", fmt.Sprintf("active-%03d", i), clock.now(), 1))
			}
			before := m.Rules("a")
			if len(before) != 128 {
				t.Fatalf("seeded rules = %d, want 128", len(before))
			}
			clock.set(testEpoch.Add(61 * time.Minute))
			if failInsert {
				if _, err := m.store.db.Exec("CREATE TRIGGER reject_new_rule BEFORE INSERT ON rules WHEN NEW.model='new-model' BEGIN SELECT RAISE(FAIL, 'test insert failure'); END"); err != nil {
					t.Fatal(err)
				}
			}
			incoming := testObservation(m, "a", "new-model", clock.now(), 1)
			m.persist(incoming)
			want := append([]Rule(nil), before...)
			if !failInsert {
				// The replacement name sorts immediately before "oldest".
				want[len(want)-1] = incoming.rule
			}
			checkRules := func(m *Manager) {
				t.Helper()
				if got := m.Rules("a"); !reflect.DeepEqual(got, want) {
					t.Fatalf("account rules changed unexpectedly: got %+v, want %+v", got, want)
				}
				if got := m.Rules("other"); !reflect.DeepEqual(got, []Rule{other.rule}) {
					t.Fatalf("reclaimed another account's expired rule: %+v", got)
				}
				_, active := m.Lookup("a", "new-model")
				if active == failInsert {
					t.Fatalf("new rule active = %v, insert failure = %v", active, failInsert)
				}
			}
			checkRules(m)
			wantErrors := uint64(0)
			if failInsert {
				wantErrors = 1
			}
			if status := m.Status(); status.StorageErrors != wantErrors || status.Dropped != 0 {
				t.Fatalf("status after reclaim = %+v", status)
			}
			m.Close()
			m = openTestManager(t, path, clock)
			checkRules(m)
		})
	}
}

func TestPersistFullActiveAccountDropsNewModelButAllowsRefresh(t *testing.T) {
	m, clock, path := newTestManager(t)
	configureTestManager(t, m, true, 292)
	other := testObservation(m, "other", "expired", testEpoch.Add(-time.Minute), 1)
	m.persist(other)
	clock.set(testEpoch.Add(30 * time.Minute))
	for i := range 128 {
		m.persist(testObservation(m, "a", fmt.Sprintf("model-%03d", i), clock.now(), 1))
	}
	before := m.Rules("a")
	if len(before) != 128 {
		t.Fatalf("seeded rules = %d, want 128", len(before))
	}
	clock.set(testEpoch.Add(85 * time.Minute))
	m.persist(testObservation(m, "a", "overflow", clock.now(), 1))
	if got := m.Rules("a"); !reflect.DeepEqual(got, before) {
		t.Fatal("full active account changed on rejected admission")
	}
	if got := m.Rules("other"); !reflect.DeepEqual(got, []Rule{other.rule}) {
		t.Fatal("full active account reclaimed another account's expired entry")
	}
	if status := m.Status(); status.Dropped != 1 || status.StorageErrors != 0 {
		t.Fatalf("rejected admission status = %+v", status)
	}
	refresh := testObservation(m, "a", "model-000", clock.now(), 1)
	m.persist(refresh)
	requireRule(t, m, refresh.rule)
	if status := m.Status(); status.Dropped != 1 || status.StorageErrors != 0 {
		t.Fatalf("refresh at capacity changed counters: %+v", status)
	}
	before[0] = refresh.rule
	m.Close()
	m = openTestManager(t, path, clock)
	if got := m.Rules("a"); !reflect.DeepEqual(got, before) {
		t.Fatal("restart did not preserve all 128 rules and the refresh")
	}
	if _, ok := m.Lookup("a", "overflow"); ok {
		t.Fatal("rejected admission persisted")
	}
}

func TestCloseContextCancellationDiscardsQueuedObservations(t *testing.T) {
	m, clock, path := newTestManager(t)
	configureTestManager(t, m, true, 292)
	baseline := testObservation(m, "a", "saved", testEpoch, 1)
	m.persist(baseline)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	closed := make(chan struct{})
	func() {
		// Prevent any queued write from winning the shutdown cancellation race.
		m.writeMu.Lock()
		defer m.writeMu.Unlock()
		for i := range 3 {
			m.Observe("a", fmt.Sprintf("queued-%d", i), testToken(testEpoch, 1))
		}
		go func() {
			m.CloseContext(ctx)
			close(closed)
		}()
		<-m.workerCtx.Done()
	}()
	<-closed
	if err := m.workerCtx.Err(); err != context.Canceled {
		t.Fatalf("worker context error = %v, want cancellation", err)
	}
	if got := m.Rules("a"); !reflect.DeepEqual(got, []Rule{baseline.rule}) {
		t.Fatalf("cancelled observations activated: %+v", got)
	}
	if status := m.Status(); status.Dropped != 0 || status.StorageErrors != 0 {
		t.Fatalf("queued cancellation reported a storage failure or overflow: %+v", status)
	}
	if m.store != nil {
		t.Fatal("CloseContext returned before closing storage")
	}
	m = openTestManager(t, path, clock)
	if got := m.Rules("a"); !reflect.DeepEqual(got, []Rule{baseline.rule}) {
		t.Fatalf("cancelled observations persisted across restart: %+v", got)
	}
	if err := m.workerCtx.Err(); err != nil {
		t.Fatalf("reopened worker context is cancelled: %v", err)
	}
	m.Observe("a", "after-restart", testToken(testEpoch, 1))
	m.Close()
	m = openTestManager(t, path, clock)
	requireRule(t, m, testObservation(m, "a", "after-restart", testEpoch, 1).rule)
}
