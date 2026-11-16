package speedtest

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
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
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.TLSClientConfig = &tls.Config{RootCAs: pool}
	pt, err := newProtoTransport(version, base, "ip", &net.Dialer{})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(srv.URL)
	pt.addServer(u.Host)
	return &http.Client{Transport: pt, Timeout: 10 * time.Second}
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

func TestHTTPVersions(t *testing.T) {
	srv := testServer(t, true, false)
	for version, want := range map[string]string{"1.1": "HTTP/1.1", "2": "HTTP/2.0", "3": "HTTP/3.0", "auto": "HTTP/2.0"} {
		t.Run(version, func(t *testing.T) {
			if got := protoOf(t, newTestClient(t, version, srv), http.MethodGet, srv.URL); got != want {
				t.Errorf("used %s, want %s", got, want)
			}
		})
	}
	if _, err := newProtoTransport("4", &http.Transport{}, "ip", &net.Dialer{}); err == nil {
		t.Error("HTTP version 4 accepted")
	}
}

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
