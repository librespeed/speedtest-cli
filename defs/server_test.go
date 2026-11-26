package defs

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// getIPHandler serves a canned getIP.php response and records the parsed result.
func getIPResultFromPayload(t *testing.T, payload string) *GetIPResult {
	t.Helper()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(payload))
	}))
	defer ts.Close()

	s := &Server{Server: ts.URL, GetIPURL: "/"}
	got, err := s.GetIPInfo("km")
	if err != nil {
		t.Fatalf("GetIPInfo returned error: %v", err)
	}
	return got
}

func TestGetIPInfoParsesObject(t *testing.T) {
	got := getIPResultFromPayload(t, `{"processedString":"1.2.3.4","rawIspInfo":{"ip":"1.2.3.4","city":"Prague","org":"ACME"}}`)

	if got.ProcessedString != "1.2.3.4" {
		t.Errorf("ProcessedString = %q, want %q", got.ProcessedString, "1.2.3.4")
	}
	if got.IP() != "1.2.3.4" {
		t.Errorf("IP() = %q, want %q", got.IP(), "1.2.3.4")
	}
}

// The rawIspInfo field comes back as an empty string from some backends
// (issue #85). The result must keep it as-is so telemetry reports an empty
// string rather than an all-empty object, and must not lose processedString.
func TestGetIPInfoKeepsEmptyRawIspInfo(t *testing.T) {
	got := getIPResultFromPayload(t, `{"processedString":"10.0.0.70","rawIspInfo":""}`)

	if got.ProcessedString != "10.0.0.70" {
		t.Errorf("ProcessedString = %q, want %q", got.ProcessedString, "10.0.0.70")
	}
	// With rawIspInfo empty the address is recovered from processedString.
	if got.IP() != "10.0.0.70" {
		t.Errorf("IP() = %q, want %q", got.IP(), "10.0.0.70")
	}

	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"processedString":"10.0.0.70","rawIspInfo":""}`
	if string(b) != want {
		t.Errorf("Marshal(GetIPResult) = %s, want %s", b, want)
	}
}

// Some backends wrap the whole payload in processedString (issue #78). The
// object inside must still surface the client address.
func TestGetIPInfoExtractsIPFromNestedPayload(t *testing.T) {
	got := getIPResultFromPayload(t, `{"processedString":"{\"ip\":\"9.9.9.9\",\"city\":\"X\"}","rawIspInfo":{"ip":"9.9.9.9","city":"X"}}`)

	if got.IP() != "9.9.9.9" {
		t.Errorf("IP() = %q, want %q", got.IP(), "9.9.9.9")
	}
}

func TestGetIPInfoAbsentRawIspInfo(t *testing.T) {
	got := getIPResultFromPayload(t, `{"processedString":"1.2.3.4"}`)

	if got.ProcessedString != "1.2.3.4" {
		t.Errorf("ProcessedString = %q, want %q", got.ProcessedString, "1.2.3.4")
	}
	// Recovered from processedString when rawIspInfo is absent entirely.
	if got.IP() != "1.2.3.4" {
		t.Errorf("IP() = %q, want %q", got.IP(), "1.2.3.4")
	}
}

func TestGetIPInfoParsesStructuredLocation(t *testing.T) {
	got := getIPResultFromPayload(t, `{
		"processedString":"128.94.2.134 - AS8075",
		"rawIspInfo":{"ip":"128.94.2.134","org":"Microsoft"},
		"location":{"city":"Boydton","region":"Virginia","country":"US","countryName":"United States","latitude":"36.6676","longitude":"-78.3875"}
	}`)

	location := got.Location
	if location == nil {
		t.Fatal("Location returned nil")
	}
	if location.City != "Boydton" || location.Region != "Virginia" ||
		location.Country != "US" || location.CountryName != "United States" {
		t.Errorf("Location = %#v", location)
	}
	if !location.ValidCoordinates() || location.Latitude != 36.6676 || location.Longitude != -78.3875 {
		t.Errorf("coordinates = (%v, %v), valid=%t", location.Latitude, location.Longitude, location.ValidCoordinates())
	}
}

func TestGetIPInfoMalformedLocationIsNonFatal(t *testing.T) {
	got := getIPResultFromPayload(t, `{
		"processedString":"192.0.2.1 - Example ISP",
		"rawIspInfo":{"ip":"192.0.2.1","org":"Example"},
		"location":{"city":"Example City","latitude":"bad","longitude":181}
	}`)

	if got.ProcessedString != "192.0.2.1 - Example ISP" {
		t.Errorf("ProcessedString = %q", got.ProcessedString)
	}
	if got.IP() != "192.0.2.1" {
		t.Errorf("IP() = %q, want 192.0.2.1", got.IP())
	}
	if got.Location == nil || got.Location.City != "Example City" {
		t.Errorf("valid location text was lost: %#v", got.Location)
	}
	if got.Location.ValidCoordinates() {
		t.Errorf("malformed coordinates accepted: %#v", got.Location)
	}
}

func TestGetIPInfoCountryOnlyLocationKeepsPartialPlace(t *testing.T) {
	got := getIPResultFromPayload(t, `{
		"processedString":"128.94.2.134 - AS8075",
		"rawIspInfo":{"ip":"128.94.2.134","city":"Boydton","region":"Virginia","country":"US","loc":"36.6676,-78.3875","org":"Microsoft"},
		"location":{"country":"US","countryName":"United States"}
	}`)

	location := got.EffectiveLocation()
	if location == nil {
		t.Fatal("EffectiveLocation returned nil")
	}
	if location.City != "Boydton" || location.Region != "Virginia" ||
		location.Country != "US" || location.CountryName != "United States" {
		t.Errorf("country-only location lost partial place: %#v", location)
	}
	if !location.ValidCoordinates() || location.Latitude != 36.6676 || location.Longitude != -78.3875 {
		t.Errorf("country-only location lost coordinates: %#v", location)
	}
}

// --- Upload against a server with librespeed-rs body semantics ---

// librespeedRSLikeServer mimics the hand-written HTTP body handling of
// librespeed/speedtest-rust: a Content-Length body is read exactly and
// answered; a chunked body is read as a raw stream in fixed 1024-byte blocks
// and never chunk-decoded. With a finite payload the chunked path blocks once
// the data runs out while the client keeps the connection open — the stall
// behind librespeed/speedtest-cli#122. It records the Content-Length header
// and total body bytes of every request it answers.
func librespeedRSLikeServer(t *testing.T) (*Server, *atomic.Int64, *atomic.Int64) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	var contentLength atomic.Int64
	var bodyBytes atomic.Int64

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				br := bufio.NewReader(conn)
				for {
					status, err := br.ReadString('\n')
					if err != nil {
						return
					}
					if !strings.HasPrefix(strings.ToLower(status), "post ") {
						return
					}
					cl := int64(0)
					chunked := false
					for {
						line, err := br.ReadString('\n')
						if err != nil {
							return
						}
						line = strings.TrimRight(line, "\r\n")
						if line == "" {
							break
						}
						k, v, _ := strings.Cut(line, ":")
						switch strings.ToLower(strings.TrimSpace(k)) {
						case "content-length":
							cl, _ = strconv.ParseInt(strings.TrimSpace(v), 10, 64)
						case "transfer-encoding":
							chunked = strings.EqualFold(strings.TrimSpace(v), "chunked")
						}
					}
					contentLength.Store(cl)
					switch {
					case cl > 0:
						// Fixed: read exactly cl bytes, then answer.
						n, err := io.CopyN(io.Discard, br, cl)
						if err != nil {
							return
						}
						bodyBytes.Add(n)
					case chunked:
						// Chunked: read the raw stream in fixed blocks, never
						// chunk-decoded (librespeed-rs behaviour). With a
						// finite payload this blocks until the peer goes away.
						buf := make([]byte, 1024)
						for {
							if _, err := io.ReadFull(br, buf); err != nil {
								break
							}
							bodyBytes.Add(1024)
						}
						return
					default:
						return
					}
					if _, err := conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")); err != nil {
						return
					}
				}
			}(conn)
		}
	}()

	return &Server{Server: "http://" + ln.Addr().String(), UploadURL: "/"}, &contentLength, &bodyBytes
}

// TestUploadSendsContentLength is the regression test for
// librespeed/speedtest-cli#122: Upload must send an explicit Content-Length
// matching the generated payload so servers that never chunk-decode
// (librespeed-rs) answer instead of blocking on a body that never ends.
// Without the fix the request goes out chunked and the upload stalls after a
// single payload; with a wrong length the transport errors out the same way.
func TestUploadSendsContentLength(t *testing.T) {
	s, contentLength, bodyBytes := librespeedRSLikeServer(t)

	// Same units as the CLI: --upload-size N means N KiB. SetUploadSize
	// scales by 1024, so the payload and the Content-Length must be 1 MiB,
	// not 1024.
	const (
		uploadSizeKiB = 1024
		payloadBytes  = uploadSizeKiB * 1024
		duration      = 500 * time.Millisecond
		requests      = 2
	)

	_, total, err := s.Upload(false, true, false, false, requests, uploadSizeKiB, duration)
	if err != nil {
		t.Fatalf("Upload returned error: %v", err)
	}

	if got := contentLength.Load(); got != payloadBytes {
		t.Errorf("server saw Content-Length %d, want %d (chunked encoding or a wrong length stalls the upload)", got, int64(payloadBytes))
	}
	if got := bodyBytes.Load(); got <= payloadBytes {
		t.Errorf("server consumed %d bytes, want more than one %d-byte payload (upload stalled)", got, int64(payloadBytes))
	}
	if total <= payloadBytes {
		t.Errorf("counter reported %d bytes, want more than one payload", total)
	}
}

// flakyUploadServer drops the connection of the first `failures` POSTs
// without answering, then answers every POST. It returns the server and the number
// of body bytes it read from the answered POSTs.
func flakyUploadServer(t *testing.T, failures int64) (*Server, *atomic.Int64) {
	t.Helper()
	var seen, answered atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen.Add(1) <= failures {
			conn, _, err := http.NewResponseController(w).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		n, _ := io.Copy(io.Discard, r.Body)
		answered.Add(n)
	}))
	t.Cleanup(ts.Close)
	return &Server{Server: ts.URL, UploadURL: "/"}, &answered
}

// A request that fails mid-test is retried instead of dropped: the body
// cannot be replayed, so the transport does not retry it, and with a single
// stream the run would otherwise upload nothing and report 0 Mbps.
func TestUploadRetriesAFailedRequest(t *testing.T) {
	s, answered := flakyUploadServer(t, 1)
	_, total, err := s.Upload(false, true, false, false, 1, 64, time.Second)
	if err != nil {
		t.Fatalf("Upload returned error: %v", err)
	}
	if total == 0 || answered.Load() == 0 {
		t.Fatalf("uploaded %d bytes, server read %d: the failed request was not retried", total, answered.Load())
	}
}

// When every request fails there is no measurement, and Upload says so
// instead of returning 0 Mbps as a result.
func TestUploadFailsWhenNothingIsUploaded(t *testing.T) {
	s, _ := flakyUploadServer(t, 1<<30)
	if _, total, err := s.Upload(false, true, false, false, 1, 64, time.Second); err == nil {
		t.Fatalf("Upload of %d bytes returned no error while every request failed", total)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// A request still in flight when the test ends must not turn into a result of
// 0 Mbps: nothing was answered, so there is no measurement to report.
func TestUploadFailsWhenNoRequestIsAnswered(t *testing.T) {
	// the transport blocks until the request's context ends, so not a byte of
	// the body is sent and no answer arrives
	blocking := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	defer func(old http.RoundTripper) { http.DefaultClient.Transport = old }(http.DefaultClient.Transport)
	http.DefaultClient.Transport = blocking

	s := &Server{Server: "http://127.0.0.1:1", UploadURL: "/"}
	rate, total, err := s.Upload(false, true, false, false, 1, 64, 500*time.Millisecond)
	if err == nil {
		t.Fatalf("Upload reported %.2f Mbps over %d bytes while no request was answered", rate, total)
	}
}

// Download has the same hole and reports it the same way.
func TestDownloadFailsWhenNoRequestIsAnswered(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(ts.Close)

	s := &Server{Server: ts.URL, DownloadURL: "/"}
	rate, total, err := s.Download(true, false, false, 1, 1, 500*time.Millisecond)
	if err == nil {
		t.Fatalf("Download reported %.2f Mbps over %d bytes while no request was answered", rate, total)
	}
}

// TestUploadCountsRequestsThatTimeOut: with --no-pre-allocate the body never
// ends, so every request ends at the client timeout. The server still got
// what was sent, and the result must count it.
func TestUploadCountsRequestsThatTimeOut(t *testing.T) {
	var received atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		received.Add(n)
	}))
	t.Cleanup(ts.Close)
	defer func(old time.Duration) { http.DefaultClient.Timeout = old }(http.DefaultClient.Timeout)
	http.DefaultClient.Timeout = 300 * time.Millisecond

	s := &Server{Server: ts.URL, UploadURL: "/"}
	_, total, err := s.Upload(true, true, false, false, 1, 64, 1500*time.Millisecond)
	if err != nil {
		t.Fatalf("Upload returned error: %v", err)
	}
	// let the server finish counting the last request
	time.Sleep(100 * time.Millisecond)
	if got := received.Load(); total < uint64(got)/2 {
		t.Fatalf("reported %d bytes while the server received %d", total, got)
	}
}

// workerRecorder records the worker each request was tagged with.
type workerRecorder struct {
	mu      sync.Mutex
	workers map[int]bool
}

func (r *workerRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	if w, ok := Worker(req.Context()); ok {
		r.mu.Lock()
		r.workers[w] = true
		r.mu.Unlock()
	}
	return http.DefaultTransport.RoundTrip(req)
}

// Download and Upload tag each request with its worker, so the transport can
// give each worker its own connection. A replacement request keeps the
// worker of the request it replaces.
func TestDownloadAndUploadTagTheirWorkers(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Write(make([]byte, 64<<10))
	}))
	t.Cleanup(ts.Close)
	defer func(old http.RoundTripper) { http.DefaultClient.Transport = old }(http.DefaultClient.Transport)

	s := &Server{Server: ts.URL, DownloadURL: "/", UploadURL: "/"}
	const requests = 3
	for name, run := range map[string]func() error{
		"download": func() error {
			_, _, err := s.Download(true, false, false, requests, 1, 500*time.Millisecond)
			return err
		},
		"upload": func() error {
			_, _, err := s.Upload(false, true, false, false, requests, 64, 500*time.Millisecond)
			return err
		},
	} {
		rec := &workerRecorder{workers: map[int]bool{}}
		http.DefaultClient.Transport = rec
		if err := run(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(rec.workers) != requests {
			t.Errorf("%s: requests tagged with workers %v, want 0 to %d", name, rec.workers, requests-1)
		}
	}
}

// readSizeRecorder records the largest read made on the bodies it returns.
type readSizeRecorder struct{ max atomic.Int64 }

func (r *readSizeRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err == nil {
		resp.Body = recordingBody{resp.Body, r}
	}
	return resp, err
}

type recordingBody struct {
	io.ReadCloser
	r *readSizeRecorder
}

func (b recordingBody) Read(p []byte) (int, error) {
	for n := int64(len(p)); ; {
		old := b.r.max.Load()
		if n <= old || b.r.max.CompareAndSwap(old, n) {
			break
		}
	}
	return b.ReadCloser.Read(p)
}

// Download reads the body in large chunks: Go's HTTP/2 client sends a
// WINDOW_UPDATE frame after every read that frees 4 KiB, so the 8 KiB reads
// of io.Copy into io.Discard cost one write for every 8 KiB downloaded.
func TestDownloadReadsInLargeChunks(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, 1<<20))
	}))
	t.Cleanup(ts.Close)
	defer func(old http.RoundTripper) { http.DefaultClient.Transport = old }(http.DefaultClient.Transport)
	rec := &readSizeRecorder{}
	http.DefaultClient.Transport = rec

	s := &Server{Server: ts.URL, DownloadURL: "/"}
	if _, _, err := s.Download(true, false, false, 1, 1, 300*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if got := rec.max.Load(); got < downloadReadSize {
		t.Fatalf("largest read asked for %d bytes, want %d", got, downloadReadSize)
	}
}
