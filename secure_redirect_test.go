package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// redirectingFront is an HTTPS server that sends the requests for the given
// paths on to a plain HTTP backend, and answers the others itself.
func redirectingFront(t *testing.T, backend *httptest.Server, paths ...string) *httptest.Server {
	t.Helper()

	front := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, path := range paths {
			if r.URL.Path == path {
				http.Redirect(w, r, backend.URL+r.URL.RequestURI(), http.StatusTemporaryRedirect)
				return
			}
		}
		if r.URL.Path == "/getIP.php" {
			w.Write([]byte(`{"processedString":"127.0.0.1 - localhost","rawIspInfo":""}`))
		}
	}))
	t.Cleanup(front.Close)
	return front
}

const refusedRedirect = "the server redirected to plain HTTP, which --secure does not allow"

// --secure asks for HTTPS to every test server. A server that answers with a
// redirect to plain HTTP must not move the test there.
func TestSecureRefusesARedirectToPlainHTTP(t *testing.T) {
	var overPlainHTTP atomic.Int64
	plain := transferBackend(t, http.StatusOK)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		overPlainHTTP.Add(1)
		plain.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(backend.Close)
	front := redirectingFront(t, backend, "/empty.php", "/getIP.php", "/garbage.php")

	list := writeServerList(t, listedServer{1, "Front", front.URL, "empty.php"})
	args := []string{"--local-json", list, "--server", "1", "--skip-cert-verify",
		"--duration", "1", "--no-icmp", "--no-download", "--no-upload", "--json"}

	// without --secure the redirect is followed, as it always was
	if r := runCLI(t, args...); r.code != 0 || overPlainHTTP.Load() == 0 {
		t.Errorf("without --secure: exit status %d and %d requests over plain HTTP, want 0 and some, stderr:\n%s", r.code, overPlainHTTP.Load(), r.stderr)
	}

	overPlainHTTP.Store(0)
	r := runCLI(t, append(args, "--secure")...)
	if r.code != 1 {
		t.Errorf("exit status %d, want 1, stderr:\n%s", r.code, r.stderr)
	}
	if r.stdout != "" {
		t.Errorf("stdout = %q, want no report", r.stdout)
	}
	if msg := r.failure(t); !strings.HasSuffix(msg, refusedRedirect) {
		t.Errorf("error = %q, want it to end in %q", msg, refusedRedirect)
	}
	if n := overPlainHTTP.Load(); n != 0 {
		t.Errorf("%d requests went on over plain HTTP, want none", n)
	}
}

// The same holds when only the download URL is redirected: the download is
// neither measured over plain HTTP nor reported as 0 Mbps.
func TestSecureRefusesARedirectedDownload(t *testing.T) {
	backend := transferBackend(t, http.StatusOK)
	front := redirectingFront(t, backend, "/garbage.php")

	list := writeServerList(t, listedServer{1, "Front", front.URL, "empty.php"})
	r := runCLI(t, "--local-json", list, "--server", "1", "--skip-cert-verify", "--secure",
		"--duration", "1", "--no-icmp", "--no-upload", "--json")

	if r.code != 1 {
		t.Errorf("exit status %d, want 1, stderr:\n%s", r.code, r.stderr)
	}
	if r.stdout != "" {
		t.Errorf("stdout = %q, want no report", r.stdout)
	}
	if msg := r.failure(t); !strings.HasSuffix(msg, refusedRedirect) {
		t.Errorf("error = %q, want it to end in %q", msg, refusedRedirect)
	}
}

// And when only the upload is redirected. An upload does not follow a
// redirect, so a 302 and a 307 both arrive as a status. Neither is a
// result, and neither sends anything on over plain HTTP.
func TestSecureRefusesARedirectedUpload(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{http.StatusFound, "the upload request returned HTTP 302 Found"},
		{http.StatusTemporaryRedirect, "the upload request returned HTTP 307 Temporary Redirect"},
	} {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			var overPlainHTTP atomic.Int64
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				overPlainHTTP.Add(1)
			}))
			t.Cleanup(backend.Close)

			front := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost:
					http.Redirect(w, r, backend.URL+r.URL.RequestURI(), tc.status)
				case r.URL.Path == "/getIP.php":
					w.Write([]byte(`{"processedString":"127.0.0.1 - localhost","rawIspInfo":""}`))
				}
			}))
			t.Cleanup(front.Close)

			list := writeServerList(t, listedServer{1, "Front", front.URL, "empty.php"})
			// the payload is small enough for the server to take in whole, so
			// that it has no unread body to close the connection on
			r := runCLI(t, "--local-json", list, "--server", "1", "--skip-cert-verify", "--secure",
				"--duration", "1", "--no-icmp", "--no-download", "--upload-size", "16", "--json")

			if r.code != 1 {
				t.Errorf("exit status %d, want 1, stderr:\n%s", r.code, r.stderr)
			}
			if r.stdout != "" {
				t.Errorf("stdout = %q, want no report", r.stdout)
			}
			if msg := r.failure(t); !strings.HasSuffix(msg, tc.want) {
				t.Errorf("error = %q, want it to end in %q", msg, tc.want)
			}
			if n := overPlainHTTP.Load(); n != 0 {
				t.Errorf("%d requests went on over plain HTTP, want none", n)
			}
		})
	}
}

// The telemetry request is made after the test, with a client of its own
// that gets the same policy, so --secure covers it as well. A telemetry
// server that sends the result on to plain HTTP gets an error line and the
// result stays off plain HTTP. The run itself has its result and is not
// failed by that.
func TestSecureRefusesARedirectedTelemetry(t *testing.T) {
	var overPlainHTTP atomic.Int64
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		overPlainHTTP.Add(1)
	}))
	t.Cleanup(plain.Close)

	telemetry := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	}))
	t.Cleanup(telemetry.Close)

	// a test server that redirects nothing
	front := redirectingFront(t, plain)
	list := writeServerList(t, listedServer{1, "Front", front.URL, "empty.php"})
	args := []string{"--local-json", list, "--server", "1", "--skip-cert-verify",
		"--duration", "1", "--no-icmp", "--no-download", "--no-upload", "--json",
		"--telemetry-server", telemetry.URL}

	// without --secure the redirect is followed, as it always was
	if r := runCLI(t, args...); r.code != 0 || overPlainHTTP.Load() == 0 {
		t.Errorf("without --secure: exit status %d and %d requests over plain HTTP, want 0 and some, stderr:\n%s",
			r.code, overPlainHTTP.Load(), r.stderr)
	}
	overPlainHTTP.Store(0)

	r := runCLI(t, append(args, "--secure")...)
	if r.code != 0 {
		t.Errorf("exit status %d, want 0, stderr:\n%s", r.code, r.stderr)
	}
	if !strings.Contains(r.stdout, `"name":"Front"`) {
		t.Errorf("stdout = %q, want the report", r.stdout)
	}
	if !strings.Contains(r.stderr, "Error when sending telemetry data: ") || !strings.Contains(r.stderr, refusedRedirect) {
		t.Errorf("stderr does not name the refused telemetry redirect:\n%s", r.stderr)
	}
	if n := overPlainHTTP.Load(); n != 0 {
		t.Errorf("%d requests went on over plain HTTP, want none", n)
	}
}
