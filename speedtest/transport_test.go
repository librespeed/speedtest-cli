package speedtest

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/quic-go/quic-go/http3"
)

// testServer serves TCP and, if h3, QUIC on the same port, replying with the
// protocol it was reached over and advertising HTTP/3 if asked to.
func testServer(t *testing.T, h3, advertise bool) *httptest.Server {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, port, _ := net.SplitHostPort(r.Host); advertise {
			w.Header().Set("Alt-Svc", `h3=":`+port+`"; ma=60`)
		}
		io.Copy(io.Discard, r.Body)
		io.WriteString(w, r.Proto)
	})
	srv := httptest.NewUnstartedServer(handler)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	if h3 {
		udp, err := net.ListenPacket("udp", srv.Listener.Addr().String())
		if err != nil {
			t.Skip(err)
		}
		s := &http3.Server{Handler: handler, TLSConfig: http3.ConfigureTLSConfig(srv.TLS.Clone())}
		go s.Serve(udp)
		t.Cleanup(func() { s.Close() })
	}
	return srv
}

func newTestClient(t *testing.T, version string, srv *httptest.Server) *http.Client {
	return newTestClientWith(t, version, srv, nil, 10*time.Second)
}

// newTestClientWith also sets the proxy the TCP transport would use.
func newTestClientWith(t *testing.T, version string, srv *httptest.Server, proxy *url.URL, timeout time.Duration) *http.Client {
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.TLSClientConfig = &tls.Config{RootCAs: pool}
	base.Proxy = nil
	if proxy != nil {
		base.Proxy = http.ProxyURL(proxy)
	}
	pt, err := newProtoTransport(version, base, "ip", &net.Dialer{}, timeout)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(srv.URL)
	pt.addServer(u.Host)
	return &http.Client{Transport: pt, Timeout: timeout}
}

func protoOf(t *testing.T, c *http.Client, method, url string) string {
	t.Helper()
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader("x")
	}
	req, _ := http.NewRequest(method, url, body)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if string(b) != resp.Proto {
		t.Fatalf("client saw %s, server saw %s", resp.Proto, b)
	}
	return resp.Proto
}

// Each forced version is used as asked, and auto stays on TCP while the
// server does not advertise HTTP/3.
func TestHTTPVersions(t *testing.T) {
	srv := testServer(t, true, false)
	for version, want := range map[string]string{"1": "HTTP/1.1", "1.1": "HTTP/1.1", "2": "HTTP/2.0", "3": "HTTP/3.0", "auto": "HTTP/2.0"} {
		t.Run(version, func(t *testing.T) {
			if got := protoOf(t, newTestClient(t, version, srv), http.MethodGet, srv.URL); got != want {
				t.Errorf("used %s, want %s", got, want)
			}
		})
	}
	if _, err := newProtoTransport("4", &http.Transport{}, "ip", &net.Dialer{}, 0); err == nil {
		t.Error("HTTP version 4 accepted")
	}
}

// A proxy keeps HTTP/3 off: auto stays on TCP, through the proxy, and a
// forced HTTP/3 fails instead of going around it. The proxy is set on the
// transport rather than with HTTPS_PROXY: http.ProxyFromEnvironment reads the
// environment only once per process, and never proxies localhost.
func TestProxyKeepsHTTP3Off(t *testing.T) {
	srv := testServer(t, true, true)
	var proxied atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
			return
		}
		proxied.Add(1)
		dst, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
		src, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			dst.Close()
			return
		}
		go func() { io.Copy(dst, src); dst.Close() }()
		io.Copy(src, dst)
		src.Close()
	}))
	t.Cleanup(proxy.Close)
	proxyURL, _ := url.Parse(proxy.URL)

	c := newTestClientWith(t, "auto", srv, proxyURL, 10*time.Second)
	for range 2 {
		if got := protoOf(t, c, http.MethodGet, srv.URL); got != "HTTP/2.0" {
			t.Fatalf("auto with a proxy used %s, want HTTP/2.0", got)
		}
	}
	if proxied.Load() == 0 {
		t.Error("auto did not go through the proxy")
	}

	c = newTestClientWith(t, "3", srv, proxyURL, 10*time.Second)
	_, err := c.Get(srv.URL)
	if err == nil || !strings.Contains(err.Error(), "proxy") {
		t.Fatalf("forced HTTP/3 with a proxy: got %v, want a proxy error", err)
	}
}

// A dual-stack host resolved IPv6 first, with an IPv4 source address, must
// still be reached over IPv4.
func TestDialQUICSkipsOtherFamily(t *testing.T) {
	srv := testServer(t, true, false)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	tlsConf := &tls.Config{RootCAs: pool, ServerName: "example.com", NextProtos: []string{http3.NextProtoH3}}
	dialer := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1")}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := dialQUICAddrs(ctx, []net.IP{net.ParseIP("::1"), net.ParseIP("127.0.0.1")}, port, dialer, tlsConf, nil)
	if err != nil {
		t.Fatal(err)
	}
	conn.CloseWithError(0, "")
}

// A failure message names the host and the cause without their control
// characters.
func TestH3FailureIsSanitized(t *testing.T) {
	msg := h3Failure("host\x1b[2J", errors.New("x509: certificate is valid for \x1b[2Jevil\n"))
	if strings.ContainsAny(msg, "\x1b\n") {
		t.Errorf("control characters kept: %q", msg)
	}
}

// HTTP/3 to an http:// server fails with a hint to use https.
func TestHTTP3NeedsHTTPS(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	pt, err := newProtoTransport("3", http.DefaultTransport.(*http.Transport).Clone(), "ip", &net.Dialer{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(srv.URL)
	pt.addServer(u.Host)
	if _, err := (&http.Client{Transport: pt}).Get(srv.URL); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("HTTP/3 over http: got %v, want an https error", err)
	}
}

// A server that advertises HTTP/3 is reached over it from the first
// request on, uploads included.
func TestAutoUpgradesOnAltSvc(t *testing.T) {
	srv := testServer(t, true, true)
	c := newTestClient(t, "auto", srv)
	if got := protoOf(t, c, http.MethodGet, srv.URL); got != "HTTP/3.0" {
		t.Fatalf("first request used %s, want HTTP/3.0", got)
	}
	if got := protoOf(t, c, http.MethodPost, srv.URL); got != "HTTP/3.0" {
		t.Fatalf("upload used %s, want HTTP/3.0", got)
	}
}

// A server that advertises HTTP/3 without serving it is reached over TCP in
// auto mode, and fails a forced HTTP/3.
func TestAutoFallsBackWhenQUICFails(t *testing.T) {
	srv := testServer(t, false, true)
	c := newTestClient(t, "auto", srv)
	for range 2 {
		if got := protoOf(t, c, http.MethodGet, srv.URL); got != "HTTP/2.0" {
			t.Fatalf("used %s, want fallback to HTTP/2.0", got)
		}
	}
	c = newTestClient(t, "3", srv)
	c.Timeout = 2 * time.Second
	if _, err := c.Get(srv.URL); err == nil {
		t.Fatal("forced HTTP/3 succeeded without a QUIC server")
	}
}

// On a network that drops UDP the handshake times out silently. auto must
// give up on it early enough to retry over TCP within the client timeout.
func TestAutoFallsBackWhenUDPIsDropped(t *testing.T) {
	srv := testServer(t, false, true)
	// bound but never read: packets vanish, like on a filtered network
	blackhole, err := net.ListenPacket("udp", srv.Listener.Addr().String())
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { blackhole.Close() })
	c := newTestClientWith(t, "auto", srv, nil, 3*time.Second)
	if got := protoOf(t, c, http.MethodGet, srv.URL); got != "HTTP/2.0" {
		t.Fatalf("used %s, want fallback to HTTP/2.0", got)
	}
}

// Only an h3 alternative on the same host and port counts, and ma=0 or
// clear withdraws it.
func TestAdvertisesH3(t *testing.T) {
	u, _ := url.Parse("https://speed.example.com:8443/")
	for header, want := range map[string]bool{
		`h3=":8443"; ma=60`:                   true,
		`h3="speed.example.com:8443"`:         true,
		`h3-29=":8443", h3=":8443"; ma=86400`: true,
		`h3=":8443"; ma=0`:                    false,
		`h3=":8443"; ma="0"`:                  false,
		`clear`:                               false,
		`h3=":8443", clear`:                   false,
		`h3=":443"; ma=60`:                    false,
		`h3="other.example.com:8443"; ma=60`:  false,
		`h2=":8443"`:                          false,
		`h3=:8443`:                            false,
		`xh3=":8443"`:                         false,
		`h3=":8443x"`:                         false,
		`h3=":8443"; ma=00`:                   false,
		`h3=":8443"; ma="00"`:                 false,
		`Clear`:                               false,
		`h3=":8443", Clear`:                   false,
		`h3=":8443"; note="x;ma=0"`:           true,
		`h3=":8443"; note="a,clear"`:          true,
	} {
		resp := &http.Response{Header: http.Header{"Alt-Svc": {header}}}
		if got := advertisesH3(resp, u); got != want {
			t.Errorf("Alt-Svc: %s: got %t, want %t", header, got, want)
		}
	}
}

// What the measurement responses ran over is reported, mixed protocols too.
func TestObservedProtocols(t *testing.T) {
	srv := testServer(t, true, true)
	c := newTestClient(t, "auto", srv)
	pt := c.Transport.(*protoTransport)
	u, _ := url.Parse(srv.URL)
	protoOf(t, c, http.MethodGet, srv.URL)
	if proto, state, ok := pt.observedOn(u.Host); !ok || proto != "HTTP/3.0" || state == nil {
		t.Fatalf("observed %q, TLS %v, %t; want HTTP/3.0 with TLS", proto, state, ok)
	}
	pt.resetObserved(u.Host)
	if _, _, ok := pt.observedOn(u.Host); ok {
		t.Fatal("observation kept after reset")
	}
	pt.setState(u.Host, "tcp")
	protoOf(t, c, http.MethodGet, srv.URL)
	pt.setState(u.Host, "h3")
	protoOf(t, c, http.MethodGet, srv.URL)
	if proto, _, _ := pt.observedOn(u.Host); proto != "HTTP/2.0+HTTP/3.0" {
		t.Fatalf("observed %q, want HTTP/2.0+HTTP/3.0", proto)
	}
}

// errors.Join of no errors is nil, and http3 dereferences the connection of
// a dial that returns a nil error.
func TestDialQUICWithoutAddresses(t *testing.T) {
	conn, err := dialQUICAddrs(context.Background(), nil, "443", &net.Dialer{}, &tls.Config{}, nil)
	if conn != nil || err == nil {
		t.Fatalf("got conn %v, error %v; want an error", conn, err)
	}
}

// openFDs counts this process's open file descriptors.
func openFDs(t *testing.T) int {
	t.Helper()
	fds, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Skip(err)
	}
	return len(fds)
}

// Every UDP socket a dial opens is closed again, whether the dial fails or
// the connection it made ends. GC is off so finalizers cannot hide a leak.
func TestDialQUICClosesItsSocket(t *testing.T) {
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	srv := testServer(t, true, false)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	// a port with nothing behind it, for the failing dials
	closed, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	_, closedPort, _ := net.SplitHostPort(closed.LocalAddr().String())
	closed.Close()

	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	tlsConf := &tls.Config{RootCAs: pool, ServerName: "example.com", NextProtos: []string{http3.NextProtoH3}}
	loopback := []net.IP{net.ParseIP("127.0.0.1")}

	before := openFDs(t)
	for range 10 {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		if conn, err := dialQUICAddrs(ctx, loopback, closedPort, &net.Dialer{}, tlsConf, nil); err == nil {
			conn.CloseWithError(0, "")
			t.Fatal("dial to a closed port succeeded")
		}
		cancel()
		conn, err := dialQUICAddrs(context.Background(), loopback, port, &net.Dialer{}, tlsConf, nil)
		if err != nil {
			t.Fatal(err)
		}
		conn.CloseWithError(0, "")
	}
	// the socket of a closed connection is closed in the background
	deadline := time.Now().Add(2 * time.Second)
	for openFDs(t) > before+2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if after := openFDs(t); after > before+2 {
		t.Fatalf("open file descriptors went from %d to %d after 20 dials", before, after)
	}
}

// Errors and body read errors from a test server reach the caller with
// their control characters removed, as every caller prints them.
func TestTransportSanitizesErrors(t *testing.T) {
	srv := testServer(t, false, false)
	c := newTestClient(t, "2", srv)
	pt := c.Transport.(*protoTransport)
	pt.tcp.DialContext = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("boom \x1b[2J forged\nline")
	}
	pt.tcp.DialTLSContext = nil
	_, err := c.Get(srv.URL)
	if err == nil || strings.ContainsAny(err.Error(), "\x1b\n") {
		t.Fatalf("got %q, want an error without control characters", err)
	}

	// the server list and telemetry go through the same cleaning
	other := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(other.Close)
	pt.base.DialContext = pt.tcp.DialContext
	if _, err := c.Get(other.URL); err == nil || strings.ContainsAny(err.Error(), "\x1b\n") {
		t.Fatalf("server list request: got %q, want an error without control characters", err)
	}

	cancelled := sanitizedError{fmt.Errorf("wrapped: %w", context.Canceled)}
	if !errors.Is(cancelled, context.Canceled) {
		t.Error("sanitizing hides context.Canceled")
	}
	body := sanitizedBody{io.NopCloser(iotest.ErrReader(errors.New("reset \x1b[2J")))}
	if _, err := body.Read(make([]byte, 1)); err == nil || strings.Contains(err.Error(), "\x1b") {
		t.Errorf("body read error %q kept its control characters", err)
	}
	body = sanitizedBody{io.NopCloser(strings.NewReader(""))}
	if _, err := body.Read(make([]byte, 1)); err != io.EOF {
		t.Errorf("body read at the end returned %v, want io.EOF itself", err)
	}
}
