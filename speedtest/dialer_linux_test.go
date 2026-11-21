package speedtest

import (
	"errors"
	"net"
	"syscall"
	"testing"
)

// A connection that cannot be bound to the interface must fail for that
// reason. Setting the mark afterwards must not replace the error, whether the
// mark itself succeeds (as root) or fails (without the privilege for it).
func TestInterfaceThatCannotBeBoundFailsTheDialDespiteTheMark(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	// dial through an interface that does not exist and return why it failed
	dial := func(fwmark int) syscall.Errno {
		dialer, err := newDialerInterfaceOrFwmarkBound("nosuchif0", fwmark)
		if err != nil {
			t.Fatalf("newDialerInterfaceOrFwmarkBound: %v", err)
		}
		conn, err := dialer.Dial("tcp", ln.Addr().String())
		if err == nil {
			conn.Close()
			t.Fatalf("mark %d: connected although the interface does not exist", fwmark)
		}
		var errno syscall.Errno
		if !errors.As(err, &errno) {
			t.Fatalf("mark %d: error = %v, want the one from the socket option", fwmark, err)
		}
		return errno
	}

	// What the binding alone answers depends on the kernel and on the
	// privileges, so that is the reference and not a fixed error.
	binding := dial(0)
	if withMark := dial(1); withMark != binding {
		t.Errorf("error = %v with a mark, want the one from the binding: %v", withMark, binding)
	}
}

// --source can be combined with --fwmark. The dialer that sets the mark has
// to keep the source address.
func TestMarkedDialerKeepsTheSourceAddress(t *testing.T) {
	source := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1")}}

	dialer, err := boundDialer(source, "", 7)
	if err != nil {
		t.Fatalf("boundDialer: %v", err)
	}
	if dialer.LocalAddr != source.LocalAddr {
		t.Errorf("LocalAddr = %v, want the source address %v", dialer.LocalAddr, source.LocalAddr)
	}
	if dialer.Control == nil {
		t.Error("the dialer does not set the mark")
	}
}
