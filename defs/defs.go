package defs

import (
	"encoding/json"
	"math"
	"net"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"
)

var (
	// values to be filled in by build script
	BuildDate   string
	ProgName    string
	ProgVersion string
	// UserAgent names the platform as well as the program; see buildUserAgent.
	UserAgent = buildUserAgent()
)

// GetIPResults represents the returned JSON from backend server's getIP.php endpoint
type GetIPResult struct {
	ProcessedString string          `json:"processedString"`
	RawISPInfo      json.RawMessage `json:"rawIspInfo"`
	Location        *LocationInfo   `json:"location,omitempty"`
}

// LocationInfo is normalized approximate IP-derived location metadata.
// It is intentionally separate from browser and GPS location.
type LocationInfo struct {
	Latitude    float64 `json:"latitude,omitempty"`
	Longitude   float64 `json:"longitude,omitempty"`
	City        string  `json:"city,omitempty"`
	Region      string  `json:"region,omitempty"`
	Country     string  `json:"country,omitempty"`
	CountryName string  `json:"countryName,omitempty"`

	// LatitudeSet and LongitudeSet distinguish an explicit zero coordinate
	// from an omitted coordinate for callers constructing location values
	// outside JSON unmarshalling.
	LatitudeSet       bool `json:"-"`
	LongitudeSet      bool `json:"-"`
	coordinatesPaired bool
}

var countryIdentityAliases = buildCountryIdentityAliases()

func buildCountryIdentityAliases() map[string]string {
	aliases := make(map[string]string)

	// Build the complete alpha-2 country-code to English-name policy from CLDR.
	// This also avoids maintaining a partial hand-written ISO country table.
	namer := display.English.Regions()
	for _, region := range language.Supported.Regions() {
		code := region.String()
		if len(code) != 2 {
			continue
		}
		code = strings.ToUpper(code)
		normalizedCode := normalizeCountryIdentity(code)
		aliases[normalizedCode] = code

		name := strings.TrimSpace(namer.Name(region))
		if name != "" {
			aliases[normalizeCountryIdentity(name)] = code
		}
	}

	// Preserve common English aliases and alpha-3 spellings outside CLDR labels.
	for alias, code := range map[string]string{
		"usa":                      "US",
		"us":                       "US",
		"united states":            "US",
		"united states of america": "US",
		"america":                  "US",
		"deu":                      "DE",
		"ger":                      "DE",
		"gbr":                      "GB",
		"uk":                       "GB",
		"great britain":            "GB",
		"fin":                      "FI",
	} {
		aliases[normalizeCountryIdentity(alias)] = code
	}
	return aliases
}

// SetLatitude records a valid latitude, including zero.
// Invalid values clear the latitude instead of preserving a misleading value.
func (l *LocationInfo) SetLatitude(latitude float64) {
	if l == nil {
		return
	}
	if isValidLatitude(latitude) {
		l.Latitude = latitude
		l.LatitudeSet = true
		return
	}
	l.Latitude = 0
	l.LatitudeSet = false
}

// SetLongitude records a valid longitude, including zero.
// Invalid values clear the longitude instead of preserving a misleading value.
func (l *LocationInfo) SetLongitude(longitude float64) {
	if l == nil {
		return
	}
	if isValidLongitude(longitude) {
		l.Longitude = longitude
		l.LongitudeSet = true
		return
	}
	l.Longitude = 0
	l.LongitudeSet = false
}

// SetCoordinates records both coordinates and marks zero values as present.
func (l *LocationInfo) SetCoordinates(latitude, longitude float64) {
	l.SetLatitude(latitude)
	l.SetLongitude(longitude)
	l.coordinatesPaired = l.ValidLatitude() && l.ValidLongitude()
}

// UnmarshalJSON accepts numeric strings and numbers for optional coordinates,
// while discarding malformed coordinates instead of failing the whole IP result.
func (l *LocationInfo) UnmarshalJSON(data []byte) error {
	*l = LocationInfo{}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" || trimmed[0] != '{' {
		return nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil
	}

	l.City = rawString(fields["city"])
	l.Region = rawString(fields["region"])
	l.Country = rawString(fields["country"])
	l.CountryName = rawString(fields["countryName"])
	if l.CountryName == "" {
		l.CountryName = rawString(fields["country_name"])
	}

	if latitude, ok := rawCoordinate(fields["latitude"]); ok && isValidLatitude(latitude) {
		l.SetLatitude(latitude)
	}
	if longitude, ok := rawCoordinate(fields["longitude"]); ok && isValidLongitude(longitude) {
		l.SetLongitude(longitude)
	}
	if !l.ValidLatitude() {
		for _, key := range []string{"lat"} {
			if latitude, ok := rawCoordinate(fields[key]); ok && isValidLatitude(latitude) {
				l.SetLatitude(latitude)
				break
			}
		}
	}
	if !l.ValidLongitude() {
		for _, key := range []string{"lon"} {
			if longitude, ok := rawCoordinate(fields[key]); ok && isValidLongitude(longitude) {
				l.SetLongitude(longitude)
				break
			}
		}
	}
	if (!l.ValidLatitude() || !l.ValidLongitude()) && len(fields["loc"]) > 0 {
		var locText string
		if err := json.Unmarshal(fields["loc"], &locText); err == nil {
			parts := strings.Split(locText, ",")
			if len(parts) == 2 {
				if !l.ValidLatitude() {
					if latitude, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64); err == nil &&
						isValidLatitude(latitude) {
						l.SetLatitude(latitude)
					}
				}
				if !l.ValidLongitude() {
					if longitude, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64); err == nil &&
						isValidLongitude(longitude) {
						l.SetLongitude(longitude)
					}
				}
			}
		}
	}
	if l.LatitudeSet && l.LongitudeSet {
		l.coordinatesPaired = true
	}

	return nil
}

// MarshalJSON emits only present, valid coordinates and the available text fields.
func (l LocationInfo) MarshalJSON() ([]byte, error) {
	wire := struct {
		Latitude    *float64 `json:"latitude,omitempty"`
		Longitude   *float64 `json:"longitude,omitempty"`
		City        string   `json:"city,omitempty"`
		Region      string   `json:"region,omitempty"`
		Country     string   `json:"country,omitempty"`
		CountryName string   `json:"countryName,omitempty"`
	}{
		City:        CleanLocationText(l.City),
		Region:      CleanLocationText(l.Region),
		Country:     CleanLocationText(l.Country),
		CountryName: CleanLocationText(l.CountryName),
	}
	if l.ValidLatitude() {
		latitude := l.Latitude
		wire.Latitude = &latitude
	}
	if l.ValidLongitude() {
		longitude := l.Longitude
		wire.Longitude = &longitude
	}
	return json.Marshal(wire)
}

// UnmarshalJSON makes location optional and malformed-safe without affecting
// the legacy processedString or rawIspInfo fields.
func (g *GetIPResult) UnmarshalJSON(data []byte) error {
	var wire struct {
		ProcessedString string          `json:"processedString"`
		RawISPInfo      json.RawMessage `json:"rawIspInfo"`
		Location        json.RawMessage `json:"location"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}

	g.ProcessedString = wire.ProcessedString
	g.RawISPInfo = wire.RawISPInfo
	g.Location = nil
	if len(wire.Location) > 0 {
		var location LocationInfo
		if err := json.Unmarshal(wire.Location, &location); err == nil && location.HasData() {
			g.Location = &location
		}
	}
	return nil
}

// CleanLocationText removes control and invisible formatting characters and
// trims surrounding whitespace so unusable text cannot suppress fallbacks.
func CleanLocationText(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, value)
	return strings.TrimSpace(value)
}

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return CleanLocationText(value)
}

func rawCoordinate(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return 0, false
	}

	var number float64
	if err := json.Unmarshal(raw, &number); err == nil {
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return 0, false
		}
		return number, true
	}

	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, false
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, false
	}
	number, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, false
	}
	return number, true
}

func isValidLatitude(latitude float64) bool {
	return latitude >= -90 && latitude <= 90
}

func isValidLongitude(longitude float64) bool {
	return longitude >= -180 && longitude <= 180
}

func normalizeCountryIdentity(value string) string {
	value = CleanLocationText(value)
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer(".", "", "_", " ", "-", " ").Replace(value)
	return strings.Join(strings.Fields(value), " ")
}

func canonicalCountryIdentity(value string) string {
	normalized := normalizeCountryIdentity(value)
	if canonical, ok := countryIdentityAliases[normalized]; ok {
		return canonical
	}
	if len(normalized) == 2 {
		return strings.ToUpper(normalized)
	}
	return normalized
}

func countryNameIsKnown(value string) bool {
	normalized := normalizeCountryIdentity(value)
	_, ok := countryIdentityAliases[normalized]
	return ok
}

func countryCodeAndNameConsistent(code, name string) bool {
	code = strings.TrimSpace(code)
	name = strings.TrimSpace(name)
	if code == "" || name == "" {
		return true
	}

	canonicalCode := canonicalCountryIdentity(code)
	canonicalName := canonicalCountryIdentity(name)
	if canonicalCode == canonicalName {
		return true
	}

	// Known country names and codes must agree. Unknown names cannot be
	// disproven by an ISO-style code alone, so they remain neutral.
	return !countryNameIsKnown(name)
}

func locationCountryIdentities(location *LocationInfo) ([]string, bool) {
	if location == nil {
		return nil, true
	}

	code := strings.TrimSpace(location.Country)
	name := strings.TrimSpace(location.CountryName)
	if code == "" && name == "" {
		return nil, true
	}
	if !countryCodeAndNameConsistent(code, name) {
		return nil, false
	}

	var identities []string
	addIdentity := func(identity string) {
		if identity == "" {
			return
		}
		for _, existing := range identities {
			if existing == identity {
				return
			}
		}
		identities = append(identities, identity)
	}
	if code != "" {
		addIdentity(canonicalCountryIdentity(code))
	}
	if name != "" {
		addIdentity(canonicalCountryIdentity(name))
	}
	return identities, true
}

func countryIdentitiesCompatible(primary, fallback *LocationInfo) bool {
	// Explicit country codes are authoritative. Never let a shared name
	// override two different nonempty country codes.
	primaryCode := ""
	fallbackCode := ""
	if primary != nil {
		primaryCode = strings.TrimSpace(primary.Country)
	}
	if fallback != nil {
		fallbackCode = strings.TrimSpace(fallback.Country)
	}
	if primaryCode != "" && fallbackCode != "" &&
		canonicalCountryIdentity(primaryCode) != canonicalCountryIdentity(fallbackCode) {
		return false
	}

	primaryIdentities, primaryValid := locationCountryIdentities(primary)
	fallbackIdentities, fallbackValid := locationCountryIdentities(fallback)
	if !primaryValid || !fallbackValid {
		return false
	}
	if len(primaryIdentities) == 0 || len(fallbackIdentities) == 0 {
		return true
	}
	for _, primaryIdentity := range primaryIdentities {
		for _, fallbackIdentity := range fallbackIdentities {
			if primaryIdentity == fallbackIdentity {
				return true
			}
		}
	}
	return false
}

func (l *LocationInfo) hasPairedCoordinates() bool {
	if l == nil {
		return false
	}
	if l.coordinatesPaired {
		return l.ValidLatitude() && l.ValidLongitude()
	}
	// Directly constructed LocationInfo values with both nonzero coordinates
	// are treated as a pair; callers with one coordinate omit the other.
	return l.ValidLatitude() && l.ValidLongitude()
}

// ValidLatitude reports whether a present latitude is within its geographic range.
func (l *LocationInfo) ValidLatitude() bool {
	if l == nil {
		return false
	}
	if l.LatitudeSet {
		return isValidLatitude(l.Latitude)
	}
	return l.Latitude != 0 && isValidLatitude(l.Latitude)
}

// ValidLongitude reports whether a present longitude is within its geographic range.
func (l *LocationInfo) ValidLongitude() bool {
	if l == nil {
		return false
	}
	if l.LongitudeSet {
		return isValidLongitude(l.Longitude)
	}
	return l.Longitude != 0 && isValidLongitude(l.Longitude)
}

// ValidCoordinates reports whether both coordinates are present and valid.
func (l *LocationInfo) ValidCoordinates() bool {
	return l != nil && l.ValidLatitude() && l.ValidLongitude()
}

// HasData reports whether the location contains displayable text or coordinates.
func (l *LocationInfo) HasData() bool {
	if l == nil {
		return false
	}
	return CleanLocationText(l.City) != "" || CleanLocationText(l.Region) != "" ||
		CleanLocationText(l.Country) != "" || CleanLocationText(l.CountryName) != "" ||
		l.ValidLatitude() || l.ValidLongitude()
}

// LocationFromRawISPInfo derives approximate location metadata from legacy
// rawIspInfo payloads. It does not alter RawISPInfo.
func LocationFromRawISPInfo(raw json.RawMessage) *LocationInfo {
	if len(raw) == 0 {
		return nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil
	}

	location := &LocationInfo{
		City:        rawString(fields["city"]),
		Region:      rawString(fields["region"]),
		Country:     rawString(fields["country"]),
		CountryName: rawString(fields["countryName"]),
	}
	if location.CountryName == "" {
		location.CountryName = rawString(fields["country_name"])
	}

	if loc, ok := fields["loc"]; ok {
		var locText string
		if err := json.Unmarshal(loc, &locText); err == nil {
			parts := strings.Split(locText, ",")
			if len(parts) == 2 {
				if latitude, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64); err == nil &&
					isValidLatitude(latitude) {
					location.SetLatitude(latitude)
				}
				if longitude, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64); err == nil &&
					isValidLongitude(longitude) {
					location.SetLongitude(longitude)
				}
				if location.ValidLatitude() && location.ValidLongitude() {
					location.coordinatesPaired = true
				}
			}
		}
	}

	for _, key := range []string{"latitude", "lat"} {
		if latitude, ok := rawCoordinate(fields[key]); ok && isValidLatitude(latitude) {
			location.SetLatitude(latitude)
			break
		}
	}
	for _, key := range []string{"longitude", "lon"} {
		if longitude, ok := rawCoordinate(fields[key]); ok && isValidLongitude(longitude) {
			location.SetLongitude(longitude)
			break
		}
	}
	if location.ValidLatitude() && location.ValidLongitude() {
		location.coordinatesPaired = true
	}

	if nested, ok := fields["location"]; ok {
		var nestedLocation LocationInfo
		if err := json.Unmarshal(nested, &nestedLocation); err == nil {
			location = MergeLocations(&nestedLocation, location)
		}
	}

	if !location.HasData() {
		return nil
	}
	return location
}

// MergeLocations combines primary structured data with legacy fallback data.
// Primary fields take precedence and fallback fills only missing fields.
// Geographic fallback fields are skipped when both country codes are present
// and disagree.
func MergeLocations(primary, fallback *LocationInfo) *LocationInfo {
	if primary == nil && fallback == nil {
		return nil
	}
	var merged LocationInfo
	if primary != nil {
		merged = *primary
	}
	merged.City = CleanLocationText(merged.City)
	merged.Region = CleanLocationText(merged.Region)
	merged.Country = CleanLocationText(merged.Country)
	merged.CountryName = CleanLocationText(merged.CountryName)
	if fallback != nil {
		fallbackCopy := *fallback
		fallbackCopy.City = CleanLocationText(fallbackCopy.City)
		fallbackCopy.Region = CleanLocationText(fallbackCopy.Region)
		fallbackCopy.Country = CleanLocationText(fallbackCopy.Country)
		fallbackCopy.CountryName = CleanLocationText(fallbackCopy.CountryName)
		compatibleFallback := countryIdentitiesCompatible(&merged, &fallbackCopy)

		// A place-only primary (city/region with no country identity) must not
		// absorb fallback geography that carries a different country identity.
		primaryPlaceOnly := merged.Country == "" && merged.CountryName == "" &&
			(merged.City != "" || merged.Region != "")
		if primaryPlaceOnly {
			fallbackIdentities, fallbackValid := locationCountryIdentities(&fallbackCopy)
			if !fallbackValid || len(fallbackIdentities) > 0 {
				compatibleFallback = false
			}
		}

		if !compatibleFallback {
			if !merged.HasData() {
				return nil
			}
			return &merged
		}

		if merged.Country == "" {
			merged.Country = fallbackCopy.Country
		}

		if merged.City == "" {
			merged.City = fallbackCopy.City
		}
		if merged.Region == "" {
			merged.Region = fallbackCopy.Region
		}
		if merged.CountryName == "" && fallbackCopy.CountryName != "" {
			merged.CountryName = fallbackCopy.CountryName
		}

		primaryHasLatitude := merged.ValidLatitude()
		primaryHasLongitude := merged.ValidLongitude()
		fallbackHasLatitude := fallbackCopy.ValidLatitude()
		fallbackHasLongitude := fallbackCopy.ValidLongitude()
		fallbackPaired := fallbackCopy.hasPairedCoordinates()

		if !primaryHasLatitude && !primaryHasLongitude {
			if fallbackPaired {
				merged.SetCoordinates(fallbackCopy.Latitude, fallbackCopy.Longitude)
			} else {
				if fallbackHasLatitude {
					merged.SetLatitude(fallbackCopy.Latitude)
				}
				if fallbackHasLongitude {
					merged.SetLongitude(fallbackCopy.Longitude)
				}
			}
		} else if primaryHasLatitude && !primaryHasLongitude &&
			fallbackPaired {
			merged.SetLongitude(fallbackCopy.Longitude)
		} else if !primaryHasLatitude && primaryHasLongitude &&
			fallbackPaired {
			merged.SetLatitude(fallbackCopy.Latitude)
		}
	}
	if !merged.HasData() {
		return nil
	}
	return &merged
}

// EffectiveLocation returns top-level location metadata merged with compatible
// legacy rawIspInfo fields for any values that remain missing.
func (g *GetIPResult) EffectiveLocation() *LocationInfo {
	if g == nil {
		return nil
	}
	return MergeLocations(g.Location, LocationFromRawISPInfo(g.RawISPInfo))
}

// IPInfoResponse represents the returned JSON from IPInfo.io's API
type IPInfoResponse struct {
	IP           string `json:"ip"`
	Hostname     string `json:"hostname"`
	City         string `json:"city"`
	Region       string `json:"region"`
	Country      string `json:"country"`
	Location     string `json:"loc"`
	Organization string `json:"org"`
	Postal       string `json:"postal"`
	Timezone     string `json:"timezone"`
	Readme       string `json:"readme,omitempty"`
}

// IP returns the client IP address, preferring rawIspInfo and falling
// back to the first token of processedString when it is a valid IP.
func (g *GetIPResult) IP() string {
	var info IPInfoResponse
	if len(g.RawISPInfo) > 0 {
		json.Unmarshal(g.RawISPInfo, &info)
	}
	if info.IP != "" {
		return info.IP
	}

	fields := strings.Fields(g.ProcessedString)
	if len(fields) > 0 && net.ParseIP(fields[0]) != nil {
		return fields[0]
	}
	return ""
}
