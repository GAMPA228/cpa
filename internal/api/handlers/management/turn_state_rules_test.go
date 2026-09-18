package management

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/authheaders"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/turnstate"
)

func TestAutomaticTurnStateRuleView(t *testing.T) {
	old := turnstate.Default
	m := turnstate.NewManager()
	turnstate.Default = m
	defer func() { m.Close(); turnstate.Default = old }()
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	if err := m.Open(path); err != nil {
		t.Fatal(err)
	}
	if err := m.Configure(turnstate.Settings{Enabled: true, MaxChars: 292}); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 217)
	data[0] = 0x80
	binary.BigEndian.PutUint64(data[1:9], uint64(time.Now().Unix()))
	m.Observe("account-a", "gpt-test", base64.URLEncoding.EncodeToString(data))
	m.Close()
	if err := m.Open(path); err != nil {
		t.Fatal(err)
	}
	manual := []authheaders.Rule{{ID: "manual", Name: "Version", Operation: "override", Value: "1"}}
	account := headerRuleAccount{AuthID: "account-a", Rules: manual, Revision: headerRulesRevision(manual)}
	view := withAutomaticHeaderRules(account)
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Rules []struct {
			ID     string
			Source string
			Active bool
			Models []string
		}
		Revision string
	}
	if err = json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Rules) != 2 || decoded.Rules[1].Source != "turn-state-auto" || !decoded.Rules[1].Active || decoded.Rules[1].Models[0] != "gpt-test" {
		t.Fatalf("invalid automatic view: %s", encoded)
	}
	if decoded.Revision != account.Revision {
		t.Fatal("automatic update invalidates unrelated manual revision")
	}
	for _, action := range []string{"delete", "save", "restart"} {
		if _, err = mutateHeaderRules(manual, headerRuleMutation{ID: decoded.Rules[1].ID, Action: action}, time.Now()); err == nil {
			t.Fatal("automatic rule writable through manual mutation")
		}
	}
	if len(withAutomaticHeaderRules(headerRuleAccount{AuthID: "account-b"}).Rules) != 0 {
		t.Fatal("automatic rule leaked to another account")
	}
	if _, err = authheaders.Decode([]authheaders.Rule{{Name: turnstate.Header, Operation: "override", Value: "unsafe"}}); err == nil {
		t.Fatal("protected header became editable")
	}
}
