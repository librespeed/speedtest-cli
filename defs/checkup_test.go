package defs

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A backend that answers its probe, but not as a backend should, is down and
// says what it answered: the status when it is wrong, even with the error page
// a web server sends along, and a body otherwise.
func TestCheckUpNamesWhatTheProbeAnswered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/empty.php":
		case "/body.php":
			w.Write([]byte("hello"))
		case "/error-page.php":
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("<html><body><h1>404 Not Found</h1></body></html>\n"))
		case "/server-error.php":
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("<html><body><h1>500 Internal Server Error</h1></body></html>\n"))
		case "/unknown-status.php":
			w.WriteHeader(599)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cases := []struct {
		ping string
		want string // "" when the backend is up
	}{
		{"empty.php", ""},
		{"body.php", "the ping request returned a non-empty body"},
		{"missing.php", "the ping request returned HTTP 404 Not Found"},
		{"error-page.php", "the ping request returned HTTP 404 Not Found"},
		{"server-error.php", "the ping request returned HTTP 500 Internal Server Error"},
		{"unknown-status.php", "the ping request returned HTTP 599"},
	}
	for _, c := range cases {
		t.Run(c.ping, func(t *testing.T) {
			s := Server{Name: "probe", Server: srv.URL, PingURL: c.ping}
			err := s.CheckUp()
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("CheckUp() = %q, want nil", err)
			case c.want != "" && (err == nil || err.Error() != c.want):
				t.Fatalf("CheckUp() = %v, want %q", err, c.want)
			}
			if up := s.IsUp(); up != (c.want == "") {
				t.Errorf("IsUp() = %t, disagrees with CheckUp() = %v", up, err)
			}
		})
	}
}

// A backend that cannot be reached is down for the reason the connection
// failed, which the caller can still inspect.
func TestCheckUpKeepsWhyTheConnectionFailed(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	s := Server{Name: "dead", Server: "http://" + addr + "/", PingURL: "empty.php"}
	err = s.CheckUp()
	var opErr *net.OpError
	if !errors.As(err, &opErr) || opErr.Op != "dial" {
		t.Fatalf("CheckUp() = %v, want the dial error", err)
	}
	if s.IsUp() {
		t.Error("IsUp() = true for a server nothing listens on")
	}
}

// A backend that does not answer within the timeout is down because of it.
func TestCheckUpReportsATimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)

	orig := http.DefaultClient
	http.DefaultClient = &http.Client{Timeout: 200 * time.Millisecond}
	defer func() { http.DefaultClient = orig }()

	s := Server{Name: "hang", Server: srv.URL, PingURL: "/hang.php"}
	err := s.CheckUp()
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("CheckUp() = %v, want a timeout", err)
	}
}
