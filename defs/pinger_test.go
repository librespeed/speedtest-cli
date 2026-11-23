package defs

import (
	"net"
	"testing"
)

// --ipv4 and --ipv6 name the family the whole test runs over, so the ICMP
// ping must not resolve its target in the other one.
func TestNewPingerResolvesWithinTheRequestedFamily(t *testing.T) {
	cases := []struct {
		host    string
		network string
		wantErr bool
	}{
		{"127.0.0.1", "ip", false},
		{"127.0.0.1", "ip4", false},
		{"127.0.0.1", "ip6", true},
		{"::1", "ip", false},
		{"::1", "ip6", false},
		{"::1", "ip4", true},
	}

	for _, c := range cases {
		t.Run(c.host+" over "+c.network, func(t *testing.T) {
			p, err := newPinger(c.host, c.network)
			if c.wantErr {
				if err == nil {
					t.Fatalf("resolved %s to %s, want an error", c.host, p.IPAddr())
				}
				return
			}
			if err != nil {
				t.Fatalf("newPinger: %v", err)
			}
			if got := p.IPAddr(); got == nil || !got.IP.Equal(net.ParseIP(c.host)) {
				t.Errorf("resolved to %v, want %s", got, c.host)
			}
		})
	}
}

// The family was ignored for a name with addresses in both: the name was
// resolved before the family was set. localhost is such a name on most
// systems. Where it has no address in a family, there is nothing to tell
// apart, and that family is skipped.
func TestNewPingerResolvesANameWithinTheRequestedFamily(t *testing.T) {
	for _, network := range []string{"ip4", "ip6"} {
		t.Run(network, func(t *testing.T) {
			if _, err := net.ResolveIPAddr(network, "localhost"); err != nil {
				t.Skipf("localhost has no %s address here: %v", network, err)
			}

			p, err := newPinger("localhost", network)
			if err != nil {
				t.Fatalf("newPinger: %v", err)
			}
			got := p.IPAddr()
			if got == nil || (got.IP.To4() != nil) != (network == "ip4") {
				t.Errorf("resolved to %v over %s", got, network)
			}
		})
	}
}
