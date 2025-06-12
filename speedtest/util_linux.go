package speedtest

import (
	"net"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func newDialerInterfaceOrFwmarkBound(iface string, fwmark int) (dialer *net.Dialer, err error) {
	// In linux there is the socket option SO_BINDTODEVICE.
	// Therefore we can really bind the socket to the device instead of binding to the address that
	// would be affected by the default routes.
	control := func(network, address string, c syscall.RawConn) error {
		var errSock error
		err := c.Control((func(fd uintptr) {
			if iface != "" {
				errSock = unix.BindToDevice(int(fd), iface)
			}

			if fwmark > 0 {
				errSock = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, fwmark)
			}
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
