package report

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNewClientOrganisation(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"old schema", `{"ip":"192.0.2.1","org":"AS64496 Example"}`, "AS64496 Example"},
		{"current ipinfo schema", `{"as_name":"O2 Czech Republic, a.s.","asn":"AS5610"}`, "O2 Czech Republic, a.s."},
		{"org takes precedence over as_name", `{"org":"Old Name","as_name":"New Name"}`, "Old Name"},
		{"neither", `{"ip":"192.0.2.1"}`, ""},
		{"empty string payload", `""`, ""},
	}

	for _, c := range cases {
		got := NewClient([]byte(c.raw)).Organization
		if got != c.want {
			t.Errorf("%s: Organization = %q, want %q", c.name, got, c.want)
		}
	}
}

// as_name is an input alias for org, not a report field: accepting it must
// not start emitting it.
func TestClientJSONDoesNotLeakASName(t *testing.T) {
	c := NewClient([]byte(`{"as_name":"O2 Czech Republic, a.s."}`))
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(b), "as_name") {
		t.Errorf("client JSON leaks as_name: %s", b)
	}
	if !strings.Contains(string(b), `"org":"O2 Czech Republic, a.s."`) {
		t.Errorf("org not filled from as_name: %s", b)
	}
}
