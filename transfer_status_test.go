package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// transferBackend answers the probe and the IP lookup, and its download URL
// with the given status.
func transferBackend(t *testing.T, downloadStatus int) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/empty.php":
		case "/getIP.php":
			w.Write([]byte(`{"processedString":"127.0.0.1 - localhost","rawIspInfo":""}`))
		case "/garbage.php":
			w.WriteHeader(downloadStatus)
			w.Write([]byte(strings.Repeat("x", 64<<10)))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A server that answers its probe but whose download URL does not exist gives
// nothing to measure. The run fails and names the status in every mode, not
// reporting the speed at which the error pages arrived.
func TestMissingDownloadURLFailsTheRun(t *testing.T) {
	backend := transferBackend(t, http.StatusNotFound)
	list := writeServerList(t, listedServer{1, "Up", backend.URL, "empty.php"})

	for _, mode := range outputModes {
		t.Run("mode="+mode, func(t *testing.T) {
			r := runCLI(t, withMode(mode, "--local-json", list, "--server", "1", "--duration", "1", "--no-icmp", "--no-upload")...)

			if r.code != 1 {
				t.Errorf("exit status %d, want 1, stderr:\n%s", r.code, r.stderr)
			}
			// the event stream has announced its phases by then
			if strings.Contains(r.stdout, "Up") || strings.Contains(r.stdout, "rate") {
				t.Errorf("stdout = %q, want no report", r.stdout)
			}
			if msg, want := r.failure(t), "Selected server Up (127.0.0.1) gave no result: the download request returned HTTP 404 Not Found"; msg != want {
				t.Errorf("error = %q, want %q", msg, want)
			}
			if strings.Contains(r.stderr, "Download rate") {
				t.Errorf("a rate is printed for the failed test:\n%s", r.stderr)
			}
			if n := strings.Count(r.stderr, "HTTP 404 Not Found"); n != 1 {
				t.Errorf("the cause is on stderr %d times, want once:\n%s", n, r.stderr)
			}
		})
	}
}

// With more than one server, one whose download fails does not take the
// results of the others with it.
func TestFailedDownloadLeavesTheRunToTheOtherServers(t *testing.T) {
	good := transferBackend(t, http.StatusOK)
	bad := transferBackend(t, http.StatusNotFound)
	list := writeServerList(t,
		listedServer{1, "Bad", bad.URL, "empty.php"},
		listedServer{2, "Good", good.URL, "empty.php"},
	)
	args := []string{"--local-json", list, "--server", "1", "--server", "2", "--duration", "1", "--no-icmp", "--no-upload"}

	r := runCLI(t, append(args, "--json")...)

	if r.code != 0 {
		t.Errorf("exit status %d, want 0, stderr:\n%s", r.code, r.stderr)
	}
	var reports []struct {
		Server struct {
			Name string `json:"name"`
		} `json:"server"`
		Download float64 `json:"download"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &reports); err != nil {
		t.Fatalf("stdout is not a JSON report: %v\n%s", err, r.stdout)
	}
	if len(reports) != 1 || reports[0].Server.Name != "Good" || reports[0].Download <= 0 {
		t.Errorf("reports = %+v, want the one of Good with a download rate", reports)
	}
	const cause = "Failed to get download speed: the download request returned HTTP 404 Not Found\n"
	if !strings.Contains(r.stderr, cause) {
		t.Errorf("stderr does not say why Bad failed:\n%s", r.stderr)
	}

	// the failed server's output is set apart from the next one's like any other
	r = runCLI(t, args...)
	if !strings.Contains(r.stderr, cause+"\nSelected server: Good ") {
		t.Errorf("no blank line between the failed server and the next:\n%s", r.stderr)
	}
}
