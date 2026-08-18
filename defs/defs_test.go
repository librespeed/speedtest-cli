package defs

import "testing"

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
