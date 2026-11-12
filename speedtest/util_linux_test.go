//go:build linux
// +build linux

package speedtest

import "testing"

func TestNewDialerInterfaceOrFwmarkBoundConfiguresResolver(t *testing.T) {
	dialer, err := newDialerInterfaceOrFwmarkBound("lo", 0)
	if err != nil {
		t.Fatalf("newDialerInterfaceOrFwmarkBound() error = %v", err)
	}
	if dialer.Resolver == nil {
		t.Fatal("dialer resolver is nil; DNS lookups would bypass the interface binding")
	}
	if !dialer.Resolver.PreferGo {
		t.Error("dialer resolver does not use the Go resolver, so it would bypass the custom dialer")
	}
	if dialer.Resolver.Dial == nil {
		t.Error("dialer resolver has no custom dial function; DNS lookups would bypass the interface binding")
	}
}
