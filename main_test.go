package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runMainEnv makes the test binary run the CLI instead of the tests, so a test
// can run it as a separate process and see its exit status, stdout and stderr
// as a script wrapping it would.
const runMainEnv = "LIBRESPEED_CLI_TEST_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(runMainEnv) == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type cliResult struct {
	code   int
	stdout string
	stderr string
}

// lastStderrLine is where a wrapping script looks for the reason a run failed.
func (r cliResult) lastStderrLine() string {
	lines := strings.Split(strings.TrimRight(r.stderr, "\n"), "\n")
	return lines[len(lines)-1]
}

func runCLI(t *testing.T, args ...string) cliResult {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, os.Args[0], args...)
	cmd.Env = append(os.Environ(), runMainEnv+"=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("running the CLI: %v", err)
	}
	return cliResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// mockBackend answers like a speed test backend, with ping URLs that answer
// the probe wrongly in the ways a misconfigured or broken one does.
func mockBackend(t *testing.T) *httptest.Server {
	t.Helper()

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/empty.php":
		case "/getIP.php":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"processedString":"127.0.0.1 - localhost","rawIspInfo":""}`))
		case "/body.php":
			w.Write([]byte("hello"))
		case "/error-page.php":
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("<html><body><h1>404 Not Found</h1></body></html>\n"))
		case "/server-error.php":
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("<html><body><h1>500 Internal Server Error</h1></body></html>\n"))
		case "/hang.php":
			select {
			case <-r.Context().Done():
			case <-release:
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})
	return srv
}

// deadURL is a server URL nothing listens on.
func deadURL(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	return "http://" + ln.Addr().String() + "/"
}

type listedServer struct {
	id   int
	name string
	url  string
	ping string
}

// writeServerList writes a server list for --local-json and returns its path.
func writeServerList(t *testing.T, servers ...listedServer) string {
	t.Helper()

	type entry struct {
		ID          int    `json:"id"`
		Name        string `json:"name"`
		Server      string `json:"server"`
		DownloadURL string `json:"dlURL"`
		UploadURL   string `json:"ulURL"`
		PingURL     string `json:"pingURL"`
		GetIPURL    string `json:"getIpURL"`
	}
	entries := []entry{}
	for _, s := range servers {
		entries = append(entries, entry{s.id, s.name, s.url, "garbage.php", "empty.php", s.ping, "getIP.php"})
	}
	b, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal server list: %v", err)
	}
	path := filepath.Join(t.TempDir(), "servers.json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write server list: %v", err)
	}
	return path
}

// A server from a cached list that cannot be reached, as on an uplink that is
// down, fails the run in every mode: exit status 1, the server and the cause
// on stderr, and no report on stdout -- not a null a script would take for a
// result.
func TestDownServerFailsTheRunInEveryMode(t *testing.T) {
	const down = "Terminated due to error: Selected server Dead (127.0.0.1) is not responding: "
	list := writeServerList(t, listedServer{1, "Dead", deadURL(t), "empty.php"})

	for _, mode := range []string{"", "--json", "--json-stream", "--csv", "--simple"} {
		t.Run("mode="+mode, func(t *testing.T) {
			args := []string{"--local-json", list, "--server", "1", "--duration", "1", "--no-icmp"}
			if mode != "" {
				args = append(args, mode)
			}
			r := runCLI(t, args...)

			if r.code != 1 {
				t.Errorf("exit status %d, want 1; stderr:\n%s", r.code, r.stderr)
			}
			if r.stdout != "" {
				t.Errorf("stdout = %q, want no report", r.stdout)
			}
			cause, ok := strings.CutPrefix(r.lastStderrLine(), down)
			// the wording of the refusal is the operating system's
			if !ok || !strings.Contains(cause, "dial tcp 127.0.0.1:") {
				t.Errorf("last stderr line = %q, want %q and the failed connection", r.lastStderrLine(), down)
			}
			if mode == "" && !strings.Contains(r.stderr, "\nSelected server Dead (127.0.0.1) is not responding at the moment, try again later\n") {
				t.Errorf("the usual line is missing from stderr:\n%s", r.stderr)
			}
		})
	}
}

// A backend that answers its probe wrongly fails the run and says what it
// answered: the status when it is wrong, even with an error page along, and
// the body otherwise.
func TestServerAnsweringItsProbeWronglyFailsTheRun(t *testing.T) {
	backend := mockBackend(t)

	for _, c := range []struct{ ping, answered string }{
		{"missing.php", "HTTP 404 Not Found"},
		{"error-page.php", "HTTP 404 Not Found"},
		{"server-error.php", "HTTP 500 Internal Server Error"},
		{"body.php", "a non-empty body"},
	} {
		t.Run(c.ping, func(t *testing.T) {
			list := writeServerList(t, listedServer{1, "Wrong", backend.URL, c.ping})
			r := runCLI(t, "--local-json", list, "--server", "1", "--no-icmp", "--json")

			if r.code != 1 {
				t.Errorf("exit status %d, want 1; stderr:\n%s", r.code, r.stderr)
			}
			if r.stdout != "" {
				t.Errorf("stdout = %q, want no report", r.stdout)
			}
			want := "Terminated due to error: Selected server Wrong (127.0.0.1) is not responding: the ping request returned " + c.answered
			if got := r.lastStderrLine(); got != want {
				t.Errorf("last stderr line = %q, want %q", got, want)
			}
		})
	}
}

// A backend that does not answer within --timeout fails the run because of it.
func TestHangingServerFailsTheRunWithTheTimeout(t *testing.T) {
	backend := mockBackend(t)
	list := writeServerList(t, listedServer{1, "Hang", backend.URL, "hang.php"})

	r := runCLI(t, "--local-json", list, "--server", "1", "--no-icmp", "--timeout", "1", "--csv")

	if r.code != 1 {
		t.Errorf("exit status %d, want 1; stderr:\n%s", r.code, r.stderr)
	}
	if r.stdout != "" {
		t.Errorf("stdout = %q, want no report", r.stdout)
	}
	const down = "Terminated due to error: Selected server Hang (127.0.0.1) is not responding: "
	if cause, ok := strings.CutPrefix(r.lastStderrLine(), down); !ok || !strings.Contains(cause, "Client.Timeout exceeded") {
		t.Errorf("last stderr line = %q, want %q and the timeout", r.lastStderrLine(), down)
	}
}

// With more than one server and none of them answering, the error names every
// one with its own cause on the one line a wrapping script shows, not only the
// last server tried.
func TestRunWhoseServersAreAllDownNamesEach(t *testing.T) {
	backend := mockBackend(t)
	list := writeServerList(t,
		listedServer{1, "Dead", deadURL(t), "empty.php"},
		listedServer{2, "Wrong", backend.URL, "error-page.php"},
	)

	r := runCLI(t, "--local-json", list, "--server", "1", "--server", "2", "--duration", "1", "--no-icmp", "--json")

	if r.code != 1 {
		t.Errorf("exit status %d, want 1; stderr:\n%s", r.code, r.stderr)
	}
	if r.stdout != "" {
		t.Errorf("stdout = %q, want no report", r.stdout)
	}
	last := r.lastStderrLine()
	causes, ok := strings.CutPrefix(last, "Terminated due to error: None of the 2 selected servers is responding: ")
	dead, wrong, split := strings.Cut(causes, "; ")
	if !ok || !split {
		t.Fatalf("last stderr line = %q, want both servers named", last)
	}
	if !strings.HasPrefix(dead, "Dead (127.0.0.1): ") || !strings.Contains(dead, "dial tcp 127.0.0.1:") {
		t.Errorf("first cause = %q, want Dead and its failed connection", dead)
	}
	if want := "Wrong (127.0.0.1): the ping request returned HTTP 404 Not Found"; wrong != want {
		t.Errorf("second cause = %q, want %q", wrong, want)
	}
}

// With more than one server, a run that measured any of them reports those
// and succeeds.
func TestDownServerAmongOthersLeavesTheRunToTheOnesThatAnswer(t *testing.T) {
	backend := mockBackend(t)
	list := writeServerList(t,
		listedServer{1, "Live", backend.URL, "empty.php"},
		listedServer{2, "Dead", deadURL(t), "empty.php"},
	)

	r := runCLI(t, "--local-json", list, "--server", "1", "--server", "2",
		"--duration", "1", "--no-download", "--no-upload", "--no-icmp", "--json")

	if r.code != 0 {
		t.Fatalf("exit status %d, want 0; stderr:\n%s", r.code, r.stderr)
	}
	var reports []struct {
		Server struct {
			Name string `json:"name"`
		} `json:"server"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &reports); err != nil {
		t.Fatalf("stdout is not a JSON report: %v\n%s", err, r.stdout)
	}
	if len(reports) != 1 || reports[0].Server.Name != "Live" {
		t.Errorf("reports = %s, want the one for Live", r.stdout)
	}
}

// A run with no server to test is not a run whose servers are down: it still
// prints the empty report and succeeds.
func TestRunWithNoServerToTestStillPrintsAnEmptyReport(t *testing.T) {
	list := writeServerList(t)

	r := runCLI(t, "--local-json", list, "--server", "-1", "--json")

	if r.code != 0 || r.stdout != "null\n" {
		t.Errorf("exit status %d, stdout %q; want 0 and null; stderr:\n%s", r.code, r.stdout, r.stderr)
	}
}

// A server list that cannot be fetched fails the run as it always did.
func TestUnreachableServerListStillFailsTheRun(t *testing.T) {
	r := runCLI(t, "--server-json", deadURL(t)+"servers.json", "--server", "1", "--json")

	if r.code != 1 {
		t.Errorf("exit status %d, want 1; stderr:\n%s", r.code, r.stderr)
	}
	if r.stdout != "" {
		t.Errorf("stdout = %q, want nothing", r.stdout)
	}
	if !strings.Contains(r.stderr, "Error when fetching server list: ") || !strings.HasPrefix(r.lastStderrLine(), "Terminated due to error: ") {
		t.Errorf("stderr does not say the list could not be fetched:\n%s", r.stderr)
	}
}
