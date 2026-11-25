package report

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/librespeed/speedtest-cli/defs"
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

func TestNewClientStructuredLocation(t *testing.T) {
	raw := json.RawMessage(`{"ip":"128.94.2.134","city":"Old City","region":"Old Region","country":"US","loc":"36.6676,-78.3875","org":"Microsoft"}`)
	structured := &defs.LocationInfo{
		City:        "Boydton",
		Region:      "Virginia",
		Country:     "US",
		CountryName: "United States",
		Latitude:    36.6676,
		Longitude:   -78.3875,
	}

	c := NewClient(raw, structured)
	if c.Location == nil {
		t.Fatal("Location returned nil")
	}
	if c.Location.City != "Boydton" || c.Location.Region != "Virginia" ||
		c.Location.Country != "US" || c.Location.CountryName != "United States" {
		t.Errorf("Location = %#v", c.Location)
	}
	if !c.Location.ValidCoordinates() {
		t.Errorf("structured coordinates not valid: %#v", c.Location)
	}

	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(b), `"location":{"latitude":36.6676,"longitude":-78.3875,"city":"Boydton","region":"Virginia","country":"US","countryName":"United States"}`) {
		t.Errorf("location JSON missing or malformed: %s", b)
	}
}

func TestNewClientLegacyRawIspLocationFallback(t *testing.T) {
	c := NewClient(json.RawMessage(`{"city":"Boydton","region":"Virginia","country":"US","loc":"36.6676,-78.3875","org":"Microsoft"}`))
	if c.Location == nil {
		t.Fatal("legacy Location returned nil")
	}
	if c.Location.City != "Boydton" || c.Location.Region != "Virginia" ||
		c.Location.Country != "US" {
		t.Errorf("legacy location text = %#v", c.Location)
	}
	if !c.Location.ValidCoordinates() {
		t.Errorf("legacy coordinates not valid: %#v", c.Location)
	}
}
