package defs

import "testing"

func TestAddressFamily(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare IPv4", "198.51.100.7", "IPv4"},
		{"IPv4 with port", "198.51.100.7:8080", "IPv4"},
		{"bare IPv6", "2001:db8::1", "IPv6"},
		// A bare IPv6 address has more colons than a host:port split allows, so
		// this is the case that breaks if the split result is trusted blindly.
		{"IPv6 with port", "[2001:db8::1]:8080", "IPv6"},
		{"IPv6 loopback", "::1", "IPv6"},
		{"IPv4 loopback", "127.0.0.1", "IPv4"},
		// Reported by the family actually carried, not by the notation.
		{"IPv4-mapped IPv6", "::ffff:198.51.100.7", "IPv4"},
		{"a hostname is not an address", "example.com", "unknown"},
		{"empty", "", "unknown"},
		{"nonsense", "not an address", "unknown"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := addressFamily(c.in); got != c.want {
				t.Errorf("addressFamily(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
