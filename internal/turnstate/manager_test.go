package turnstate

import (
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

var testEpoch = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

// These envelopes intentionally have no valid signature: Parse only checks structure.
func testToken(issued time.Time, blocks int) string {
	data := make([]byte, 57+16*blocks)
	data[0] = 0x80
	binary.BigEndian.PutUint64(data[1:9], uint64(issued.Unix()))
	return base64.URLEncoding.EncodeToString(data)
}

func TestNewManagerDefaults(t *testing.T) {
	m := NewManager()
	if got := m.Status(); !reflect.DeepEqual(got, Status{Settings: Settings{MaxChars: 292, LifetimeSeconds: 3600, AccountScope: "all", AuthIDs: []string{}}}) {
		t.Fatalf("default status = %+v", got)
	}
	m.Observe("account", "model", testToken(testEpoch, 1))
	if _, ok := m.Lookup("account", "model"); ok {
		t.Fatal("unopened, disabled manager activated a rule")
	}
}

func TestValue(t *testing.T) {
	for _, tt := range []struct {
		name    string
		headers http.Header
		want    string
	}{
		{"missing", nil, ""},
		{"empty slice", http.Header{Header: {}}, ""},
		{"blank", http.Header{Header: {" \t\r\n"}}, ""},
		{"trim", http.Header{Header: {" \tencoded-token\r\n"}}, "encoded-token"},
		{"case insensitive", http.Header{"x-cOdEx-TuRn-StAtE": {"token"}}, "token"},
		{"unrelated", http.Header{"Other": {"ignored"}, Header: {"token"}}, "token"},
		{"multiple", http.Header{Header: {"one", "two"}}, ""},
		{"identical multiple", http.Header{Header: {"one", "one"}}, ""},
		{"multiple casing", http.Header{Header: {"one"}, "x-codex-turn-state": {"two"}}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, length := Value(tt.headers)
			if got != tt.want {
				t.Fatalf("value = %q, want %q", got, tt.want)
			}
			if tt.want == "" {
				if length != nil {
					t.Fatalf("rejected value has length %d", *length)
				}
			} else if length == nil || *length != len(tt.want) {
				t.Fatalf("length = %v, want %d", length, len(tt.want))
			}
		})
	}
}

func TestParseFernetEnvelope(t *testing.T) {
	valid := testToken(testEpoch, 1)
	mutate := func(version byte, size int, seconds uint64) string {
		data := make([]byte, size)
		data[0] = version
		binary.BigEndian.PutUint64(data[1:9], seconds)
		return base64.URLEncoding.EncodeToString(data)
	}
	for _, tt := range []struct {
		name, value string
		now, issued time.Time
		valid       bool
	}{
		{"padded", valid, testEpoch, testEpoch, true},
		{"raw", strings.TrimRight(valid, "="), testEpoch, testEpoch, true},
		{"multiple blocks", testToken(testEpoch, 4), testEpoch, testEpoch, true},
		{"largest fitting envelope", testToken(testEpoch, 380), testEpoch, testEpoch, true},
		{"oversized envelope", testToken(testEpoch, 381), testEpoch, time.Time{}, false},
		{"empty", "", testEpoch, time.Time{}, false},
		{"invalid base64", "!" + valid[1:], testEpoch, time.Time{}, false},
		{"noncanonical padding bits", valid[:len(valid)-3] + "B==", testEpoch, time.Time{}, false},
		{"newline", valid + "\n", testEpoch, time.Time{}, false},
		{"space", " " + valid, testEpoch, time.Time{}, false},
		{"tab", valid + "\t", testEpoch, time.Time{}, false},
		{"old version", mutate(0x7f, 73, uint64(testEpoch.Unix())), testEpoch, time.Time{}, false},
		{"new version", mutate(0x81, 73, uint64(testEpoch.Unix())), testEpoch, time.Time{}, false},
		{"no ciphertext", mutate(0x80, 57, uint64(testEpoch.Unix())), testEpoch, time.Time{}, false},
		{"short block", mutate(0x80, 72, uint64(testEpoch.Unix())), testEpoch, time.Time{}, false},
		{"partial extra block", mutate(0x80, 74, uint64(testEpoch.Unix())), testEpoch, time.Time{}, false},
		{"just before expiry", valid, testEpoch.Add(time.Hour - time.Nanosecond), testEpoch, true},
		{"at expiry", valid, testEpoch.Add(time.Hour), time.Time{}, false},
		{"old", valid, testEpoch.Add(2 * time.Hour), time.Time{}, false},
		{"future allowance", testToken(testEpoch.Add(time.Minute), 1), testEpoch, testEpoch.Add(time.Minute), true},
		{"too far future", testToken(testEpoch.Add(61*time.Second), 1), testEpoch, time.Time{}, false},
		{"timestamp overflow", mutate(0x80, 73, ^uint64(0)), testEpoch, time.Time{}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			issued, ok := Parse(tt.value, tt.now)
			if ok != tt.valid {
				t.Fatalf("valid = %v, want %v", ok, tt.valid)
			}
			if ok && !issued.Equal(tt.issued) {
				t.Fatalf("issued = %v, want %v", issued, tt.issued)
			}
		})
	}
}
