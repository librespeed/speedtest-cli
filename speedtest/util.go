//go:build !linux
// +build !linux

package speedtest

import (
	"fmt"
	"net"
)

func newDialerInterfaceOrFwmarkBound(iface string, fwmark int) (dialer *net.Dialer, err error) {
	return nil, fmt.Errorf("cannot bound to interface on this platform")
}
