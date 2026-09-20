package turnstate

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAccountScopeFiltersCaptureLookupAndRefresh(t *testing.T) {
	m, clock, path := newTestManager(t)
	configureTestManager(t, m, true, 292)
	for _, id := range []string{"a", "b"} {
		m.persist(testObservation(m, id, "model", testEpoch, 1))
	}
	queued := testObservation(m, "b", "queued", testEpoch, 1)
	settings := Settings{Enabled: true, MaxChars: 292, AccountScope: "selected", AuthIDs: []string{"a"}}
	if err := m.Configure(settings); err != nil {
		t.Fatal(err)
	}
	settings.AuthIDs[0] = "b"
	snapshot := m.Status()
	snapshot.AuthIDs[0] = "b"
	if !m.AccountEnabled("a") || m.AccountEnabled("b") {
		t.Fatal("settings ownership or scope leaked")
	}
	if _, ok := m.Lookup("b", "model"); ok {
		t.Fatal("excluded account applied rule")
	}
	if len(m.Rules("b")) != 1 {
		t.Fatal("excluded account lost history")
	}
	clock.set(testEpoch.Add(56 * time.Minute))
	value, omit, refresh := m.PrepareRequest("b", "model")
	if value != "" || omit || refresh != nil {
		t.Fatal("excluded account changed headers or reserved refresh")
	}
	_, omit, refresh = m.PrepareRequest("a", "model")
	if !omit || refresh == nil {
		t.Fatal("included account did not refresh")
	}
	refresh.Finish(false)
	m.persist(queued)
	m.Observe("b", "blocked", testToken(clock.now(), 1))
	m.Observe("a", "accepted", testToken(clock.now(), 1))
	m.Close()
	m = openTestManager(t, path, clock)
	if len(m.Rules("b")) != 1 || len(m.Rules("a")) != 2 || m.AccountEnabled("b") {
		t.Fatal("scope or capture did not survive restart")
	}
	if err := m.Configure(Settings{Enabled: true, MaxChars: 292, AccountScope: "selected"}); err != nil {
		t.Fatal(err)
	}
	if m.AccountEnabled("a") || m.AccountEnabled("b") {
		t.Fatal("empty selected scope widened to all")
	}
	configureTestManager(t, m, true, 292)
	if _, ok := m.Lookup("b", "model"); !ok {
		t.Fatal("still-valid history was not restored")
	}
	clock.set(testEpoch.Add(time.Hour))
	if _, ok := m.Lookup("b", "model"); ok {
		t.Fatal("scope change extended expiry")
	}
}

func TestAccountScopeLegacyStoreAndAtomicFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("CREATE TABLE settings(id INTEGER PRIMARY KEY, enabled INTEGER NOT NULL, max_chars INTEGER NOT NULL); INSERT INTO settings VALUES(1,1,312)")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	clock := &testClock{}
	clock.set(testEpoch)
	m := openTestManager(t, path, clock)
	if got := m.Status(); !got.Enabled || got.MaxChars != 312 || got.AccountScope != "all" || !m.AccountEnabled("any") {
		t.Fatal("legacy defaults changed")
	}
	before := m.Status().Settings
	if _, err = m.store.db.Exec("CREATE TRIGGER reject_scope BEFORE UPDATE ON account_scope BEGIN SELECT RAISE(FAIL, 'test'); END"); err != nil {
		t.Fatal(err)
	}
	if err = m.Configure(Settings{Enabled: false, MaxChars: 292, AccountScope: "selected", AuthIDs: []string{"a"}}); err == nil {
		t.Fatal("failed storage reported success")
	}
	if !reflect.DeepEqual(before, m.Status().Settings) {
		t.Fatal("failed storage changed memory")
	}
	m.Close()
	m = openTestManager(t, path, clock)
	if !reflect.DeepEqual(before, m.Status().Settings) {
		t.Fatal("settings transaction partially committed")
	}
}

func TestAccountScopeValidation(t *testing.T) {
	for _, settings := range []Settings{
		{MaxChars: 292, AccountScope: "invalid"},
		{MaxChars: 292, AuthIDs: []string{""}},
		{MaxChars: 292, AuthIDs: []string{" a"}},
		{MaxChars: 292, AuthIDs: []string{"a\nb"}},
		{MaxChars: 292, AuthIDs: []string{strings.Repeat("a", 513)}},
		{MaxChars: 292, AuthIDs: make([]string, 4097)},
	} {
		if _, err := NormalizeSettings(settings); err == nil {
			t.Fatalf("accepted invalid scope settings: %+v", settings)
		}
	}
	got, err := NormalizeSettings(Settings{MaxChars: 292, AccountScope: "selected", AuthIDs: []string{"b", "a", "b"}})
	if err != nil || !reflect.DeepEqual(got.AuthIDs, []string{"a", "b"}) {
		t.Fatal("IDs not normalized")
	}
}

func TestAccountScopeLegacyPatchMergesUnderWriteLock(t *testing.T) {
	m, _, _ := newTestManager(t)
	configureTestManager(t, m, true, 292)
	m.writeMu.Lock()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- m.PatchSettings(true, 312, nil, nil)
	}()
	<-started
	selected, err := NormalizeSettings(Settings{Enabled: true, MaxChars: 292, AccountScope: "selected", AuthIDs: []string{"a"}})
	if err == nil {
		err = m.configureLocked(selected)
	}
	m.writeMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if status := m.Status(); status.AccountScope != "selected" || status.MaxChars != 312 || m.AccountEnabled("b") {
		t.Fatal("legacy patch overwrote scope committed ahead of it")
	}
}
