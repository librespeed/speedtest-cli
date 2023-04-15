package speedtest

import (
	"net"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func newDialerInterfaceBound(iface string) (dialer *net.Dialer, err error) {
	// In linux there is the socket option SO_BINDTODEVICE.
	// Therefore we can really bind the socket to the device instead of binding to the address that
	// would be affected by the default routes.
	control := func(network, address string, c syscall.RawConn) error {
		var errSock error
		err := c.Control((func(fd uintptr) {
			errSock = unix.BindToDevice(int(fd), iface)
		}))
		if err != nil {
			return err
		}
		return errSock
	}

	dialer = &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   control,
	}
	return dialer, nil
}
