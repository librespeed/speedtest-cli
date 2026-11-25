package defs

import (
	"encoding/json"
	"testing"
)

// The address falls back to processedString whenever rawIspInfo carries none:
// live backends answer with an empty string, a JSON null, or -- with the
// current ipinfo schema -- an object that has no ip field at all.
func TestIPRecoveredFromProcessedString(t *testing.T) {
	cases := []struct {
		name      string
		processed string
		raw       string
		want      string
	}{
		{"bare IPv6, ISP detection off", "2a00:1028:8388:a84e:8cda:7223:57f8:67a6", `""`, "2a00:1028:8388:a84e:8cda:7223:57f8:67a6"},
		{"IPv4 with ISP suffix", "192.0.2.1 - Example ISP, CZ", `""`, "192.0.2.1"},
		{"tab-separated ISP suffix", "192.0.2.1\t- Example ISP", `""`, "192.0.2.1"},
		{"not an address", "no address here", `""`, ""},
		{"empty processedString", "", `""`, ""},
		{"raw is JSON null", "192.0.2.1 - Example ISP", `null`, "192.0.2.1"},
		{"raw ISP info without IP", "192.0.2.1 - Example ISP", `{"as_name":"Example ISP","asn":"AS123"}`, "192.0.2.1"},
	}

	for _, c := range cases {
		g := GetIPResult{ProcessedString: c.processed, RawISPInfo: []byte(c.raw)}
		if got := g.IP(); got != c.want {
			t.Errorf("%s: IP() = %q, want %q", c.name, got, c.want)
		}
	}
}

// The raw field is the backend's own answer; the token is only a stand-in.
func TestIPPrefersRawOverProcessedString(t *testing.T) {
	g := GetIPResult{
		ProcessedString: "198.51.100.7 - X",
		RawISPInfo:      []byte(`{"ip":"192.0.2.1"}`),
	}
	if got := g.IP(); got != "192.0.2.1" {
		t.Errorf("IP() = %q, want %q", got, "192.0.2.1")
	}
}

func TestLocationFromStructuredAndLegacyMetadata(t *testing.T) {
	payload := []byte(`{
		"processedString":"128.94.2.134 - AS8075",
		"rawIspInfo":{"ip":"128.94.2.134","city":"Old City","region":"Old Region","country":"US","loc":"36.6676,-78.3875","org":"Microsoft"},
		"location":{"city":"Boydton","region":"Virginia","country":"US","countryName":"United States","latitude":"36.6676","longitude":"-78.3875"}
	}`)

	var got GetIPResult
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.ProcessedString != "128.94.2.134 - AS8075" {
		t.Errorf("ProcessedString = %q", got.ProcessedString)
	}
	if got.IP() != "128.94.2.134" {
		t.Errorf("IP() = %q, want 128.94.2.134", got.IP())
	}

	location := got.EffectiveLocation()
	if location == nil {
		t.Fatal("EffectiveLocation returned nil")
	}
	if location.City != "Boydton" || location.Region != "Virginia" ||
		location.Country != "US" || location.CountryName != "United States" {
		t.Errorf("location text = %#v", location)
	}
	if !location.ValidCoordinates() || location.Latitude != 36.6676 || location.Longitude != -78.3875 {
		t.Errorf("coordinates = (%v, %v), valid=%t", location.Latitude, location.Longitude, location.ValidCoordinates())
	}
}

func TestMalformedAndMissingLocationDoNotBreakGetIPResult(t *testing.T) {
	cases := []struct {
		name     string
		location string
	}{
		{"malformed object", `{"city":"Example","latitude":"not-a-number","longitude":181}`},
		{"string", `"Example"`},
		{"missing", ``},
		{"null", `null`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			payload := `{"processedString":"192.0.2.1 - Example ISP","rawIspInfo":""`
			if c.location != "" {
				payload += `,"location":` + c.location
			}
			payload += `}`

			var got GetIPResult
			if err := json.Unmarshal([]byte(payload), &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got.ProcessedString != "192.0.2.1 - Example ISP" {
				t.Errorf("ProcessedString = %q", got.ProcessedString)
			}
			if got.IP() != "192.0.2.1" {
				t.Errorf("IP() = %q, want 192.0.2.1", got.IP())
			}
			if c.location == `{"city":"Example","latitude":"not-a-number","longitude":181}` {
				if got.Location == nil || got.Location.City != "Example" {
					t.Errorf("text location was lost: %#v", got.Location)
				}
				if got.Location.ValidCoordinates() {
					t.Errorf("malformed coordinates accepted: %#v", got.Location)
				}
			}
		})
	}
}

func TestLocationFromLegacyRawIspInfo(t *testing.T) {
	location := LocationFromRawISPInfo(json.RawMessage(`{
		"ip":"128.94.2.134",
		"city":"Boydton",
		"region":"Virginia",
		"country":"US",
		"loc":"36.6676,-78.3875",
		"org":"Microsoft"
	}`))
	if location == nil {
		t.Fatal("legacy location returned nil")
	}
	if location.City != "Boydton" || location.Region != "Virginia" || location.Country != "US" {
		t.Errorf("legacy text = %#v", location)
	}
	if !location.ValidCoordinates() || location.Latitude != 36.6676 || location.Longitude != -78.3875 {
		t.Errorf("legacy coordinates = (%v, %v)", location.Latitude, location.Longitude)
	}
}
