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
