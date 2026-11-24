package speedtest

import (
	"errors"
	"net"
	"syscall"
	"testing"
	"time"
)

// A connection that cannot be bound to the interface must fail for that
// reason, with or without a mark. The mark is not set once the binding has
// failed, so it cannot replace the error.
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

// What counts is where the connection comes from and what its socket
// carries. The dialer that boundDialer returns has to connect from the
// source address it was given, whether it sets a mark or binds to an
// interface, and the mark has to be on the socket. Setting a mark needs a
// privilege that a test run usually does not have, and that case is then
// skipped. So is the interface case on a kernel that wants a privilege for
// the binding as well.
func TestBoundDialerConnectsFromTheSourceAddress(t *testing.T) {
	const source = "127.0.0.2"

	for _, tc := range []struct {
		name   string
		iface  string
		fwmark int
	}{
		{"with a mark", "", 7},
		{"bound to the loopback interface", "lo", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			defer ln.Close()

			from := make(chan string, 1)
			go func() {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				host, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
				conn.Close()
				from <- host
			}()

			dialer, err := boundDialer(&net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(source)}}, tc.iface, tc.fwmark)
			if err != nil {
				t.Fatalf("boundDialer: %v", err)
			}
			conn, err := dialer.Dial("tcp", ln.Addr().String())
			if errors.Is(err, syscall.EPERM) {
				t.Skipf("not permitted here: %v", err)
			}
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer conn.Close()

			if tc.fwmark != 0 {
				if got := socketMark(t, conn); got != tc.fwmark {
					t.Errorf("the socket carries mark %d, want %d", got, tc.fwmark)
				}
			}

			select {
			case got := <-from:
				if got != source {
					t.Errorf("the connection came from %s, want %s", got, source)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the connection was not accepted")
			}
		})
	}
}

// socketMark reads the mark back from the socket of a connection.
func socketMark(t *testing.T, conn net.Conn) int {
	t.Helper()

	raw, err := conn.(*net.TCPConn).SyscallConn()
	if err != nil {
		t.Fatalf("SyscallConn: %v", err)
	}
	var mark int
	var errSock error
	if err := raw.Control(func(fd uintptr) {
		mark, errSock = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK)
	}); err != nil {
		t.Fatalf("Control: %v", err)
	}
	if errSock != nil {
		t.Fatalf("reading the mark: %v", errSock)
	}
	return mark
}
