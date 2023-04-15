package speedtest

import (
	"fmt"
	"net"
)

func newDialerInterfaceBound(iface string) (dialer *net.Dialer, err error) {
	return nil, fmt.Errorf("cannot bound to interface on Windows")
}
