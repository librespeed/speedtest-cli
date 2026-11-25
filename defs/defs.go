package defs

import (
	"encoding/json"
	"math"
	"net"
	"strconv"
	"strings"
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

	latitudeSet  bool
	longitudeSet bool
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
		l.Latitude = latitude
		l.latitudeSet = true
	}
	if longitude, ok := rawCoordinate(fields["longitude"]); ok && isValidLongitude(longitude) {
		l.Longitude = longitude
		l.longitudeSet = true
	}
	if !l.ValidLatitude() {
		for _, key := range []string{"lat"} {
			if latitude, ok := rawCoordinate(fields[key]); ok && isValidLatitude(latitude) {
				l.Latitude = latitude
				l.latitudeSet = true
				break
			}
		}
	}
	if !l.ValidLongitude() {
		for _, key := range []string{"lon"} {
			if longitude, ok := rawCoordinate(fields[key]); ok && isValidLongitude(longitude) {
				l.Longitude = longitude
				l.longitudeSet = true
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
						l.Latitude = latitude
						l.latitudeSet = true
					}
				}
				if !l.ValidLongitude() {
					if longitude, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64); err == nil &&
						isValidLongitude(longitude) {
						l.Longitude = longitude
						l.longitudeSet = true
					}
				}
			}
		}
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
		City:        l.City,
		Region:      l.Region,
		Country:     l.Country,
		CountryName: l.CountryName,
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

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
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

// ValidLatitude reports whether a present latitude is within its geographic range.
func (l *LocationInfo) ValidLatitude() bool {
	if l == nil {
		return false
	}
	if l.latitudeSet {
		return isValidLatitude(l.Latitude)
	}
	return l.Latitude != 0 && isValidLatitude(l.Latitude)
}

// ValidLongitude reports whether a present longitude is within its geographic range.
func (l *LocationInfo) ValidLongitude() bool {
	if l == nil {
		return false
	}
	if l.longitudeSet {
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
	return l.City != "" || l.Region != "" || l.Country != "" || l.CountryName != "" ||
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
					location.Latitude = latitude
					location.latitudeSet = true
				}
				if longitude, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64); err == nil &&
					isValidLongitude(longitude) {
					location.Longitude = longitude
					location.longitudeSet = true
				}
			}
		}
	}

	for _, key := range []string{"latitude", "lat"} {
		if latitude, ok := rawCoordinate(fields[key]); ok && isValidLatitude(latitude) {
			location.Latitude = latitude
			location.latitudeSet = true
			break
		}
	}
	for _, key := range []string{"longitude", "lon"} {
		if longitude, ok := rawCoordinate(fields[key]); ok && isValidLongitude(longitude) {
			location.Longitude = longitude
			location.longitudeSet = true
			break
		}
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
func MergeLocations(primary, fallback *LocationInfo) *LocationInfo {
	if primary == nil && fallback == nil {
		return nil
	}
	var merged LocationInfo
	if primary != nil {
		merged = *primary
	}
	if fallback != nil {
		if merged.City == "" {
			merged.City = fallback.City
		}
		if merged.Region == "" {
			merged.Region = fallback.Region
		}
		if merged.Country == "" {
			merged.Country = fallback.Country
		}
		if merged.CountryName == "" {
			merged.CountryName = fallback.CountryName
		}
		if !merged.ValidLatitude() && fallback.ValidLatitude() {
			merged.Latitude = fallback.Latitude
			merged.latitudeSet = fallback.latitudeSet
		}
		if !merged.ValidLongitude() && fallback.ValidLongitude() {
			merged.Longitude = fallback.Longitude
			merged.longitudeSet = fallback.longitudeSet
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
