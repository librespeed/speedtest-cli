package defs

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The negotiated parameters must reach the Server for the report, and stay
// absent over plain HTTP.
func TestIsUpCapturesNegotiatedTLS(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	tlsSrv := httptest.NewTLSServer(handler)
	defer tlsSrv.Close()

	// IsUp uses http.DefaultClient; trust the test server's certificate
	// for the duration of the test.
	orig := http.DefaultClient
	http.DefaultClient = tlsSrv.Client()
	defer func() { http.DefaultClient = orig }()

	s := Server{Name: "tls", Server: tlsSrv.URL, PingURL: "/empty"}

	if !s.IsUp() {
		t.Fatal("TLS test server reported down")
	}
	if s.NegotiatedTLS == nil {
		t.Fatal("NegotiatedTLS not captured over HTTPS")
	}
	if !strings.HasPrefix(s.NegotiatedTLS.Version, "TLS") || s.NegotiatedTLS.Cipher == "" {
		t.Fatalf("implausible negotiation: %+v", s.NegotiatedTLS)
	}

	plainSrv := httptest.NewServer(handler)
	defer plainSrv.Close()

	p := Server{Name: "plain", Server: plainSrv.URL, PingURL: "/empty"}

	if !p.IsUp() {
		t.Fatal("plain test server reported down")
	}
	if p.NegotiatedTLS != nil {
		t.Fatalf("NegotiatedTLS set over plain HTTP: %+v", p.NegotiatedTLS)
	}
}
