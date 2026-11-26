package defs

import (
	"encoding/json"
	"strings"
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

func TestWhitespaceAndInvisibleLocationFallsBackToLegacy(t *testing.T) {
	var got GetIPResult
	payload := []byte(`{
		"processedString":"128.94.2.134 - AS8075",
		"rawIspInfo":{"ip":"128.94.2.134","city":"Boydton","region":"Virginia","country":"US","countryName":"United States","loc":"36.6676,-78.3875","org":"Microsoft"},
		"location":{"city":"   ","region":"\u200b","country":"\t","countryName":"\n"}
	}`)
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	location := got.EffectiveLocation()
	if location == nil {
		t.Fatal("EffectiveLocation returned nil")
	}
	if location.City != "Boydton" || location.Region != "Virginia" ||
		location.Country != "US" || location.CountryName != "United States" {
		t.Errorf("whitespace location did not fall back: %#v", location)
	}
	if !location.ValidCoordinates() || location.Latitude != 36.6676 || location.Longitude != -78.3875 {
		t.Errorf("whitespace location lost coordinates: %#v", location)
	}
}

func TestFiniteCoordinateFallbacksRejectInvalidPrimaryValues(t *testing.T) {
	var got GetIPResult
	payload := []byte(`{
		"processedString":"128.94.2.134 - AS8075",
		"rawIspInfo":{"ip":"128.94.2.134","loc":"36.6676,-78.3875","org":"Microsoft"},
		"location":{"city":"Example City","latitude":91,"longitude":"Infinity","lat":38.1234,"lon":-77.1234}
	}`)
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	location := got.EffectiveLocation()
	if location == nil {
		t.Fatal("EffectiveLocation returned nil")
	}
	if location.City != "Example City" {
		t.Errorf("place metadata lost: %#v", location)
	}
	if !location.ValidCoordinates() || location.Latitude != 38.1234 || location.Longitude != -77.1234 {
		t.Errorf("finite fallback coordinates not used: %#v", location)
	}
}

func TestLocationMarshalPreservesRawAndLegacyFields(t *testing.T) {
	got := GetIPResult{
		ProcessedString: "128.94.2.134 - AS8075",
		RawISPInfo:      json.RawMessage(`{"ip":"128.94.2.134","city":"Boydton","org":"Microsoft"}`),
		Location: &LocationInfo{
			City:        "Boydton",
			Country:     "US",
			CountryName: "United States",
			Latitude:    36.6676,
			Longitude:   -78.3875,
		},
	}

	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	text := string(b)
	for _, want := range []string{`"processedString":"128.94.2.134 - AS8075"`, `"rawIspInfo":{"ip":"128.94.2.134","city":"Boydton","org":"Microsoft"}`, `"location":{"latitude":36.6676,"longitude":-78.3875,"city":"Boydton","country":"US","countryName":"United States"}`} {
		if !strings.Contains(text, want) {
			t.Errorf("marshal lacks %s: %s", want, text)
		}
	}
}

func TestLocationCoordinatesCanPreserveExplicitZero(t *testing.T) {
	location := &LocationInfo{}
	location.SetCoordinates(0, 10)

	if !location.ValidLatitude() || location.Latitude != 0 || !location.LatitudeSet {
		t.Errorf("explicit latitude zero not preserved: %#v", location)
	}
	if !location.ValidLongitude() || location.Longitude != 10 || !location.LongitudeSet {
		t.Errorf("longitude not preserved: %#v", location)
	}

	b, err := json.Marshal(location)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if got, want := string(b), `{"latitude":0,"longitude":10}`; got != want {
		t.Errorf("Marshal(location) = %s, want %s", got, want)
	}
}

func TestLocationLongitudeAloneDoesNotInferLatitude(t *testing.T) {
	location := &LocationInfo{Longitude: 10}

	if location.ValidLatitude() || location.LatitudeSet {
		t.Errorf("omitted latitude inferred as present: %#v", location)
	}
	location.SetLatitude(91)
	if location.ValidLatitude() || location.LatitudeSet {
		t.Errorf("invalid latitude remained present after SetLatitude: %#v", location)
	}

	b, err := json.Marshal(location)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if got, want := string(b), `{"longitude":10}`; got != want {
		t.Errorf("Marshal(location) = %s, want %s", got, want)
	}
}

func TestLocationJSONPreservesExplicitZeroCoordinates(t *testing.T) {
	var got GetIPResult
	if err := json.Unmarshal([]byte(`{"processedString":"192.0.2.1","rawIspInfo":"","location":{"latitude":0,"longitude":10}}`), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Location == nil || !got.Location.ValidLatitude() || got.Location.Latitude != 0 ||
		!got.Location.LatitudeSet {
		t.Errorf("decoded zero latitude not preserved: %#v", got.Location)
	}
	if got.Location == nil || !got.Location.ValidLongitude() || got.Location.Longitude != 10 ||
		!got.Location.LongitudeSet {
		t.Errorf("decoded longitude not preserved: %#v", got.Location)
	}
}

func TestMergeLocationsDoesNotReplaceExplicitZeroCoordinate(t *testing.T) {
	primary := &LocationInfo{}
	primary.SetCoordinates(0, 10)
	fallback := LocationFromRawISPInfo(json.RawMessage(`{"latitude":36.6676,"longitude":-78.3875}`))

	merged := MergeLocations(primary, fallback)
	if merged == nil {
		t.Fatal("merged location returned nil")
	}
	if !merged.ValidLatitude() || merged.Latitude != 0 {
		t.Errorf("explicit zero latitude replaced: %#v", merged)
	}
	if !merged.ValidLongitude() || merged.Longitude != 10 {
		t.Errorf("primary longitude replaced: %#v", merged)
	}
}

func TestMergeLocationsFillsOmittedLatitude(t *testing.T) {
	primary := &LocationInfo{Longitude: 10}
	fallback := &LocationInfo{Latitude: 36.6676, Longitude: -78.3875}

	merged := MergeLocations(primary, fallback)
	if merged == nil {
		t.Fatal("merged location returned nil")
	}
	if !merged.ValidLatitude() || merged.Latitude != 36.6676 {
		t.Errorf("omitted latitude not filled: %#v", merged)
	}
	if !merged.ValidLongitude() || merged.Longitude != 10 {
		t.Errorf("primary longitude not preserved: %#v", merged)
	}
}

func TestMergeLocationsDoesNotCopyCountryNameForDifferentCountry(t *testing.T) {
	primary := &LocationInfo{City: "Berlin", Country: "DE"}
	fallback := &LocationInfo{
		City:        "Boydton",
		Region:      "Virginia",
		Country:     "US",
		CountryName: "United States",
		Latitude:    36.6676,
		Longitude:   -78.3875,
	}

	merged := MergeLocations(primary, fallback)
	if merged == nil {
		t.Fatal("merged location returned nil")
	}
	if merged.Country != "DE" {
		t.Errorf("merged country = %q, want DE", merged.Country)
	}
	if merged.CountryName != "" {
		t.Errorf("fallback country name crossed countries: %#v", merged)
	}
	if merged.City != "Berlin" || merged.Region != "" {
		t.Errorf("fallback place crossed countries: %#v", merged)
	}
	if merged.ValidCoordinates() {
		t.Errorf("fallback coordinates crossed countries: %#v", merged)
	}
}

func TestMergeLocationsCopiesMatchingCountryName(t *testing.T) {
	primary := &LocationInfo{Country: "DE"}
	fallback := &LocationInfo{Country: "de", CountryName: "Germany"}

	merged := MergeLocations(primary, fallback)
	if merged == nil {
		t.Fatal("merged location returned nil")
	}
	if merged.CountryName != "Germany" {
		t.Errorf("matching country name not copied: %#v", merged)
	}
}

func TestMergeLocationsRejectsDifferentExplicitCodesWithSharedName(t *testing.T) {
	primary := &LocationInfo{
		City:        "Berlin",
		Country:     "DE",
		CountryName: "Deutschland",
	}
	fallback := &LocationInfo{
		City:        "Boydton",
		Region:      "Virginia",
		Country:     "US",
		CountryName: "Deutschland",
		Latitude:    36.6676,
		Longitude:   -78.3875,
	}

	merged := MergeLocations(primary, fallback)
	if merged == nil {
		t.Fatal("merged location returned nil")
	}
	if merged.Country != "DE" || merged.CountryName != "Deutschland" {
		t.Errorf("primary country identity changed: %#v", merged)
	}
	if merged.City != "Berlin" || merged.Region != "" {
		t.Errorf("fallback place crossed differing explicit codes: %#v", merged)
	}
	if merged.ValidCoordinates() {
		t.Errorf("fallback coordinates crossed differing explicit codes: %#v", merged)
	}
}

func TestMergeLocationsFinlandNameAndCodeAreEquivalent(t *testing.T) {
	primary := &LocationInfo{CountryName: "Finland"}
	fallback := &LocationInfo{
		City:      "Helsinki",
		Region:    "Uusimaa",
		Country:   "FI",
		Latitude:  60.1699,
		Longitude: 24.9384,
	}

	merged := MergeLocations(primary, fallback)
	if merged == nil {
		t.Fatal("merged location returned nil")
	}
	if merged.Country != "FI" {
		t.Errorf("compatible Finland country code not copied: %#v", merged)
	}
	if merged.CountryName != "Finland" {
		t.Errorf("primary Finland country name changed: %#v", merged)
	}
	if merged.City != "Helsinki" || merged.Region != "Uusimaa" {
		t.Errorf("compatible Finland place not merged: %#v", merged)
	}
	if !merged.ValidCoordinates() || merged.Latitude != 60.1699 || merged.Longitude != 24.9384 {
		t.Errorf("compatible Finland coordinates not merged: %#v", merged)
	}
}

func TestMergeLocationsCountryNameOnlyRejectsConflictingFallback(t *testing.T) {
	primary := &LocationInfo{
		City:        "Washington",
		Region:      "District of Columbia",
		CountryName: "United States",
	}
	fallback := &LocationInfo{
		City:        "Berlin",
		Region:      "Berlin",
		Country:     "DE",
		CountryName: "Germany",
		Latitude:    52.52,
		Longitude:   13.405,
	}

	merged := MergeLocations(primary, fallback)
	if merged == nil {
		t.Fatal("merged location returned nil")
	}
	if merged.Country != "" {
		t.Errorf("fallback country code crossed country-name-only primary: %#v", merged)
	}
	if merged.CountryName != "United States" {
		t.Errorf("primary country name changed: %#v", merged)
	}
	if merged.City != "Washington" || merged.Region != "District of Columbia" {
		t.Errorf("fallback place crossed country-name-only primary: %#v", merged)
	}
	if merged.ValidCoordinates() {
		t.Errorf("fallback coordinates crossed country-name-only primary: %#v", merged)
	}
}

func TestMergeLocationsCountryNameOnlyAcceptsCompatibleFallback(t *testing.T) {
	primary := &LocationInfo{
		City:        "Washington",
		Region:      "District of Columbia",
		CountryName: "United States",
	}
	fallback := &LocationInfo{
		City:        "Boydton",
		Region:      "Virginia",
		Country:     "US",
		CountryName: "United States",
		Latitude:    36.6676,
		Longitude:   -78.3875,
	}

	merged := MergeLocations(primary, fallback)
	if merged == nil {
		t.Fatal("merged location returned nil")
	}
	if merged.Country != "US" {
		t.Errorf("compatible fallback country code not copied: %#v", merged)
	}
	if merged.CountryName != "United States" {
		t.Errorf("compatible primary country name changed: %#v", merged)
	}
	if merged.City != "Washington" || merged.Region != "District of Columbia" {
		t.Errorf("compatible merge replaced primary place: %#v", merged)
	}
	if !merged.ValidCoordinates() || merged.Latitude != 36.6676 || merged.Longitude != -78.3875 {
		t.Errorf("compatible fallback coordinates not merged: %#v", merged)
	}
}

func TestMergeLocationsCountryNameOnlyAcceptsAbsentFallbackIdentity(t *testing.T) {
	primary := &LocationInfo{
		City:        "Washington",
		CountryName: "United States",
	}
	fallback := &LocationInfo{
		City:      "Boston",
		Region:    "Massachusetts",
		Latitude:  42.3601,
		Longitude: -71.0589,
	}

	merged := MergeLocations(primary, fallback)
	if merged == nil {
		t.Fatal("merged location returned nil")
	}
	if merged.Country != "" || merged.CountryName != "United States" {
		t.Errorf("fallback identity changed primary country identity: %#v", merged)
	}
	if merged.City != "Washington" || merged.Region != "Massachusetts" {
		t.Errorf("fallback place/region merge unexpected: %#v", merged)
	}
	if !merged.ValidCoordinates() || merged.Latitude != 42.3601 || merged.Longitude != -71.0589 {
		t.Errorf("identity-absent fallback coordinates not merged: %#v", merged)
	}
}

func TestMergeLocationsPlaceOnlyRejectsCountryIdentityFallback(t *testing.T) {
	primary := &LocationInfo{City: "Berlin"}
	fallback := &LocationInfo{
		City:        "Boydton",
		Region:      "Virginia",
		Country:     "US",
		CountryName: "United States",
		Latitude:    36.6676,
		Longitude:   -78.3875,
	}

	merged := MergeLocations(primary, fallback)
	if merged == nil {
		t.Fatal("merged location returned nil")
	}
	if merged.Country != "" || merged.CountryName != "" {
		t.Errorf("fallback country identity crossed place-only primary: %#v", merged)
	}
	if merged.City != "Berlin" || merged.Region != "" {
		t.Errorf("fallback place crossed place-only primary: %#v", merged)
	}
	if merged.ValidCoordinates() {
		t.Errorf("fallback coordinates crossed place-only primary: %#v", merged)
	}
}

func TestMergeLocationsCountryNameOnlyRejectsInconsistentFallback(t *testing.T) {
	primary := &LocationInfo{CountryName: "United States"}
	fallback := &LocationInfo{
		City:        "Boydton",
		Region:      "Virginia",
		Country:     "US",
		CountryName: "Germany",
		Latitude:    36.6676,
		Longitude:   -78.3875,
	}

	merged := MergeLocations(primary, fallback)
	if merged == nil {
		t.Fatal("merged location returned nil")
	}
	if merged.Country != "" || merged.City != "" || merged.Region != "" ||
		merged.ValidCoordinates() {
		t.Errorf("inconsistent fallback geography copied: %#v", merged)
	}
	if merged.CountryName != "United States" {
		t.Errorf("primary country name changed: %#v", merged)
	}
}

func TestMergeLocationsDoesNotMixUnpairedCoordinates(t *testing.T) {
	t.Run("primary latitude only", func(t *testing.T) {
		primary := &LocationInfo{Latitude: 36.6676}
		fallback := &LocationInfo{Longitude: -78.3875}

		merged := MergeLocations(primary, fallback)
		if merged == nil {
			t.Fatal("merged location returned nil")
		}
		if !merged.ValidLatitude() || merged.Latitude != 36.6676 {
			t.Errorf("primary latitude changed: %#v", merged)
		}
		if merged.ValidLongitude() {
			t.Errorf("unpaired fallback longitude was mixed into primary: %#v", merged)
		}
	})

	t.Run("primary longitude only", func(t *testing.T) {
		primary := &LocationInfo{Longitude: -78.3875}
		fallback := &LocationInfo{Latitude: 36.6676}

		merged := MergeLocations(primary, fallback)
		if merged == nil {
			t.Fatal("merged location returned nil")
		}
		if merged.ValidLatitude() {
			t.Errorf("unpaired fallback latitude was mixed into primary: %#v", merged)
		}
		if !merged.ValidLongitude() || merged.Longitude != -78.3875 {
			t.Errorf("primary longitude changed: %#v", merged)
		}
	})
}
