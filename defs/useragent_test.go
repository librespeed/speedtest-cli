package defs

import (
	"strings"
	"testing"
)

func TestParseOsReleaseID(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"openwrt", "ID=\"openwrt\"\nVERSION_ID=\"24.10.1\"\n", "openwrt"},
		{"unquoted", "ID=debian\nVERSION_ID=12\n", "debian"},
		// ID_LIKE and VERSION_ID must not be mistaken for ID.
		{"id_like first", "ID_LIKE=debian\nID=ubuntu\n", "ubuntu"},
		{"version only", "VERSION_ID=1.0\n", ""},
		{"empty", "", ""},
		{"garbage id", "ID=\"???\"\n", ""},
		{"uppercase and junk", "ID='OpenWrt!'\n", "openwrt"},
	}

	for _, c := range cases {
		if got := parseOsReleaseID(c.content); got != c.want {
			t.Errorf("%s: parseOsReleaseID = %q, want %q", c.name, got, c.want)
		}
	}
}

// The value lands in an HTTP header and comes from a root-writable file.
func TestParseOsReleaseIDIsBounded(t *testing.T) {
	long := "ID=" + strings.Repeat("a", 200) + "\n"
	if got := parseOsReleaseID(long); len(got) != 24 {
		t.Errorf("parseOsReleaseID length = %d, want 24", len(got))
	}
}
