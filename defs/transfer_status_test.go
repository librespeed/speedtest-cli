package defs

import (
	"context"
	"fmt"
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

// refusingServer answers every transfer request with an error page and
// counts the requests.
func refusingServer(t *testing.T, status int) (*Server, *atomic.Int64) {
	t.Helper()

	var requests atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(status)
		w.Write([]byte(strings.Repeat("<p>this is an error page</p>\n", 100)))
	}))
	t.Cleanup(ts.Close)

	return &Server{Server: ts.URL, DownloadURL: "garbage.php", UploadURL: "empty.php"}, &requests
}

// A download URL that answers 404 gives nothing to measure. The test must
// fail with the status instead of timing the error pages, and must not ask
// again as fast as the server can refuse.
func TestDownloadFailsWhenEveryRequestIsRefused(t *testing.T) {
	s, requests := refusingServer(t, http.StatusNotFound)

	_, total, err := s.Download(true, false, false, 2, 1, time.Second)

	if err == nil || err.Error() != "the download request returned HTTP 404 Not Found" {
		t.Errorf("error = %v, want the 404 named", err)
	}
	if total != 0 {
		t.Errorf("counted %d bytes of error pages as downloaded", total)
	}
	if n := requests.Load(); n > 50 {
		t.Errorf("%d requests in a second, want a pause between refused ones", n)
	}
}

func TestUploadFailsWhenEveryRequestIsRefused(t *testing.T) {
	s, requests := refusingServer(t, http.StatusRequestEntityTooLarge)

	_, _, err := s.Upload(false, true, false, false, 2, 16, time.Second)

	if err == nil || err.Error() != "the upload request returned HTTP 413 Request Entity Too Large" {
		t.Errorf("error = %v, want the 413 named", err)
	}
	if n := requests.Load(); n > 50 {
		t.Errorf("%d requests in a second, want a pause between refused ones", n)
	}
}

// A refused request is asked again after a pause. The retries must not each
// open a connection of their own: a short error page that says how long it
// is gets read, so the connection it came over can be used again.
func TestRefusedRequestsKeepTheirConnections(t *testing.T) {
	const streams = 2

	for _, phase := range []string{"download", "upload"} {
		t.Run(phase, func(t *testing.T) {
			var requests, connections atomic.Int64
			page := strings.Repeat("<p>this is an error page</p>\n", 100)
			ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Length", strconv.Itoa(len(page)))
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(page))
			}))
			ts.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateNew {
					connections.Add(1)
				}
			}
			ts.Start()
			defer ts.Close()

			s := &Server{Server: ts.URL, DownloadURL: "garbage.php", UploadURL: "empty.php"}
			if phase == "download" {
				s.Download(true, false, false, streams, 1, time.Second)
			} else {
				s.Upload(false, true, false, false, streams, 16, time.Second)
			}

			// one more than the streams leaves room for a connection that
			// was being opened when another one became free
			if r, c := requests.Load(), connections.Load(); r <= streams+1 || c > streams+1 {
				t.Errorf("%d requests over %d connections, want the %d streams to keep theirs", r, c, streams)
			}
		})
	}
}

// One refused request among good ones does not fail the test, and its error
// page is not counted as data.
func TestDownloadDoesNotCountAnErrorPage(t *testing.T) {
	const data = "0123456789"
	var requests atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			// a length the data cannot add up to
			w.Write([]byte(strings.Repeat("x", 4096+3)))
			return
		}
		w.Write([]byte(data))
	}))
	defer ts.Close()

	s := &Server{Server: ts.URL, DownloadURL: "garbage.php"}
	_, total, err := s.Download(true, false, false, 1, 1, time.Second)

	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if total == 0 || total%uint64(len(data)) != 0 {
		t.Errorf("counted %d bytes, want only whole %d-byte answers", total, len(data))
	}
}

// A download URL whose connections are dropped gives nothing to measure
// either. The test fails with the cause instead of reporting 0 Mbps.
func TestDownloadFailsWhenEveryRequestIsDropped(t *testing.T) {
	var requests atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		conn.(*net.TCPConn).SetLinger(0)
		conn.Close()
	}))
	defer ts.Close()

	s := &Server{Server: ts.URL, DownloadURL: "garbage.php"}
	_, total, err := s.Download(true, false, false, 2, 1, time.Second)

	if err == nil {
		t.Error("no error, want the dropped connection reported")
	}
	if total != 0 {
		t.Errorf("counted %d bytes", total)
	}
	if n := requests.Load(); n > 50 {
		t.Errorf("%d requests in a second, want a pause between failed ones", n)
	}
}

// A download whose answers are cut short is a result all the same: what
// arrived did cross the link. On a slow link the end of the test cuts every
// request, and none ever gets its whole body.
func TestDownloadCountsAnswersThatAreCutShort(t *testing.T) {
	chunk := make([]byte, 16<<10)

	for _, tc := range []struct {
		name string
		send http.HandlerFunc
	}{
		{"by the end of the test", func(w http.ResponseWriter, r *http.Request) {
			for r.Context().Err() == nil {
				w.Write(chunk)
				w.(http.Flusher).Flush()
				time.Sleep(20 * time.Millisecond)
			}
		}},
		{"by the server", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "1048576")
			w.Write(chunk)
			w.(http.Flusher).Flush()
			// each aborted answer costs a connection, so they are spaced
			time.Sleep(5 * time.Millisecond)
			panic(http.ErrAbortHandler)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(tc.send)
			defer ts.Close()

			s := &Server{Server: ts.URL, DownloadURL: "garbage.php"}
			_, total, err := s.Download(true, false, false, 2, 1, 500*time.Millisecond)

			if err != nil {
				t.Errorf("Download returned error: %v", err)
			}
			if total == 0 {
				t.Error("nothing was counted")
			}
		})
	}
}

// An upload is accepted once the server answers it with success. The server
// has the payload then, so an answer that is cut short after that does not
// make the request a failed one.
func TestUploadCountsARequestWhoseAnswerIsCutShort(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Length", "100")
		w.Write([]byte("ok"))
		w.(http.Flusher).Flush()
		// each aborted answer costs a connection, so they are spaced
		time.Sleep(5 * time.Millisecond)
		panic(http.ErrAbortHandler)
	}))
	defer ts.Close()

	s := &Server{Server: ts.URL, UploadURL: "empty.php"}
	_, total, err := s.Upload(false, true, false, false, 2, 16, 500*time.Millisecond)

	if err != nil {
		t.Errorf("Upload returned error: %v", err)
	}
	if total == 0 {
		t.Error("nothing was counted")
	}
}

// A download URL that answers with success and no body has nothing to
// measure either, and is not asked again without a pause.
func TestDownloadFailsWhenTheAnswersAreEmpty(t *testing.T) {
	var requests atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
	}))
	defer ts.Close()

	s := &Server{Server: ts.URL, DownloadURL: "garbage.php"}
	_, total, err := s.Download(true, false, false, 2, 1, time.Second)

	if err == nil || err.Error() != "the download request returned no data" {
		t.Errorf("error = %v, want the empty answers reported", err)
	}
	if total != 0 {
		t.Errorf("counted %d bytes", total)
	}
	if n := requests.Load(); n > 50 {
		t.Errorf("%d requests in a second, want a pause between empty ones", n)
	}
}

// clientTimeout sets the timeout of the HTTP client for one test.
func clientTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := http.DefaultClient.Timeout
	http.DefaultClient.Timeout = d
	t.Cleanup(func() { http.DefaultClient.Timeout = old })
}

// An upload URL that nothing listens on gives nothing to measure. The test
// fails with the cause instead of reporting 0 Mbps.
func TestUploadFailsWhenNoRequestReachesTheServer(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	s := &Server{Server: "http://" + addr, UploadURL: "empty.php"}
	_, total, err := s.Upload(false, true, false, false, 2, 16, time.Second)

	if err == nil {
		t.Error("no error, want the refused connection reported")
	}
	if total != 0 {
		t.Errorf("counted %d bytes", total)
	}
}

// An upload URL whose connections are dropped fails the same way. The
// client cannot send a body twice, so each dropped request is replaced by a
// new one, after a pause.
func TestUploadFailsWhenEveryRequestIsDropped(t *testing.T) {
	var requests atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		conn.(*net.TCPConn).SetLinger(0)
		conn.Close()
	}))
	defer ts.Close()

	s := &Server{Server: ts.URL, UploadURL: "empty.php"}
	_, _, err := s.Upload(false, true, false, false, 2, 16, time.Second)

	if err == nil {
		t.Error("no error, want the dropped connection reported")
	}
	if n := requests.Load(); n <= 2 || n > 50 {
		t.Errorf("%d requests in a second, want the dropped ones replaced after a pause", n)
	}
}

// The time a request takes to connect is not time spent sending. A slow
// connect in front of the dropped connections does not make them a
// measurement.
func TestUploadFailsWhenEveryRequestIsDroppedAfterASlowConnect(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		conn.(*net.TCPConn).SetLinger(0)
		conn.Close()
	}))
	defer ts.Close()

	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			select {
			case <-time.After(1200 * time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}
	t.Cleanup(func() { http.DefaultClient.Transport = old })

	s := &Server{Server: ts.URL, UploadURL: "empty.php"}
	_, _, err := s.Upload(false, true, false, false, 1, 16, 2*time.Second)

	if err == nil {
		t.Error("no error, want the dropped connection reported")
	}
}

// neverSends stands for an address that swallows connection attempts: no
// request sent through it gets as far as sending anything.
type neverSends struct{}

func (neverSends) RoundTrip(req *http.Request) (*http.Response, error) {
	<-req.Context().Done()
	return nil, req.Context().Err()
}

// An upload that sent nothing at all is not a result of 0 Mbps, whether its
// requests ran into the client timeout or were still waiting when the test
// ended.
func TestUploadFailsWhenNothingIsSent(t *testing.T) {
	for name, timeout := range map[string]time.Duration{
		"the requests time out": 200 * time.Millisecond,
		"the test ends first":   5 * time.Second,
	} {
		t.Run(name, func(t *testing.T) {
			clientTimeout(t, timeout)
			old := http.DefaultClient.Transport
			http.DefaultClient.Transport = neverSends{}
			t.Cleanup(func() { http.DefaultClient.Transport = old })

			s := &Server{Server: "http://127.0.0.1:1", UploadURL: "empty.php"}
			_, total, err := s.Upload(false, true, false, false, 2, 16, time.Second)

			if err == nil {
				t.Error("no error, want the missing answers reported")
			}
			if total != 0 {
				t.Errorf("counted %d bytes", total)
			}
		})
	}
}

// slowConn is a connection that sends 4 KiB per pause, the way a slow
// uplink does: what is written to it gets through a little at a time.
type slowConn struct {
	net.Conn
	pause time.Duration
}

func (c slowConn) Write(p []byte) (int, error) {
	for sent := 0; sent < len(p); {
		n, err := c.Conn.Write(p[sent:min(sent+4<<10, len(p))])
		sent += n
		if err != nil {
			return sent, err
		}
		time.Sleep(c.pause)
	}
	return len(p), nil
}

// slowUplink sends the requests of one test over slowConn.
func slowUplink(t *testing.T, pause time.Duration) {
	t.Helper()
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return slowConn{conn, pause}, nil
		},
	}
	t.Cleanup(func() { http.DefaultClient.Transport = old })
}

// On a slow uplink no payload gets through before the test or the client
// timeout cuts its request off, so no request is ever answered. What was
// sent is the measurement all the same.
func TestUploadCountsWhatASlowUplinkSent(t *testing.T) {
	for name, timeout := range map[string]time.Duration{
		"the requests time out": 1100 * time.Millisecond,
		"the test ends first":   5 * time.Second,
	} {
		t.Run(name, func(t *testing.T) {
			clientTimeout(t, timeout)
			// about 200 KiB/s
			slowUplink(t, 20*time.Millisecond)

			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
			}))
			defer ts.Close()

			// a payload that takes far longer than the test
			const payload = 32 << 20
			s := &Server{Server: ts.URL, UploadURL: "empty.php"}
			_, total, err := s.Upload(false, true, false, false, 2, payload>>10, 1500*time.Millisecond)

			if err != nil {
				t.Errorf("Upload returned error: %v", err)
			}
			if total == 0 || total >= 2*payload {
				t.Errorf("counted %d bytes, want a part of the two payloads", total)
			}
		})
	}
}

// An upload URL that takes the payload and never answers leaves nothing to
// measure. A payload that was handed over at once is not being sent any
// more: the request only waits, to the end of the test or to the client
// timeout. That is a failure, even though the bytes did go out.
func TestUploadFailsWhenThePayloadIsTakenAndNotAnswered(t *testing.T) {
	for name, timeout := range map[string]time.Duration{
		"the requests time out": 400 * time.Millisecond,
		"the test ends first":   5 * time.Second,
	} {
		t.Run(name, func(t *testing.T) {
			clientTimeout(t, timeout)
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				<-r.Context().Done()
			}))
			defer ts.Close()

			s := &Server{Server: ts.URL, UploadURL: "empty.php"}
			_, total, err := s.Upload(false, true, false, false, 1, 16, time.Second)

			if err == nil {
				t.Error("no error, want the missing answers reported")
			}
			if total != 0 {
				t.Errorf("counted %d bytes", total)
			}
		})
	}
}

// Such a payload is not thrown away, though, when another stream is still
// sending. It may as well sit in the buffers of a slow uplink, and the
// stream that is still sending makes the upload a measurement.
func TestUploadKeepsAHandedOverPayloadBesideAStreamThatSends(t *testing.T) {
	clientTimeout(t, time.Second)
	// handed over well within sendingProof
	const payload = 4 << 20

	// The first connection is fast. The others send about 200 KiB/s, so
	// they are still sending at the end and add far less than a payload.
	var dials atomic.Int64
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
			if err != nil || dials.Add(1) == 1 {
				return conn, err
			}
			return slowConn{conn, 20 * time.Millisecond}, nil
		},
	}
	t.Cleanup(func() { http.DefaultClient.Transport = old })

	var requests atomic.Int64
	done := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if requests.Add(1) == 1 {
			// the first payload is taken and never answered
			select {
			case <-done:
			case <-r.Context().Done():
			}
		}
	}))
	defer ts.Close()

	s := &Server{Server: ts.URL, UploadURL: "empty.php"}
	_, total, err := s.Upload(false, true, false, false, 2, payload>>10, 2*time.Second)
	close(done)

	if err != nil {
		t.Errorf("Upload returned error: %v", err)
	}
	if total < payload {
		t.Errorf("counted %d bytes, want the %d of the payload that timed out kept", total, payload)
	}
}

// With --no-pre-allocate the body never ends, so no request is ever answered
// either, and the server still got what was sent.
func TestUploadCountsAStreamThatNeverEnds(t *testing.T) {
	for name, timeout := range map[string]time.Duration{
		"the requests time out": 1100 * time.Millisecond,
		"the test ends first":   5 * time.Second,
	} {
		t.Run(name, func(t *testing.T) {
			clientTimeout(t, timeout)
			var received atomic.Int64
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n, _ := io.Copy(io.Discard, r.Body)
				received.Add(n)
			}))

			s := &Server{Server: ts.URL, UploadURL: "empty.php"}
			_, total, err := s.Upload(true, true, false, false, 2, 16, 1500*time.Millisecond)
			// the server has counted every request once it is closed
			ts.Close()

			if err != nil {
				t.Errorf("Upload returned error: %v", err)
			}
			// the count runs ahead of the server by what is on the way
			if got := uint64(received.Load()); got == 0 || got < total/2 {
				t.Errorf("counted %d bytes while the server received %d", total, got)
			}
		})
	}
}

// cuttingServer takes the body of each request for half a second and then
// answers with the given status. It counts the requests and what it read.
func cuttingServer(t *testing.T, status int) (*Server, *atomic.Int64, *atomic.Int64) {
	t.Helper()

	var requests, received atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		buf := make([]byte, 64<<10)
		for end := time.Now().Add(500 * time.Millisecond); time.Now().Before(end); {
			n, err := r.Body.Read(buf)
			received.Add(int64(n))
			if err != nil {
				break
			}
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(ts.Close)

	return &Server{Server: ts.URL, UploadURL: "empty.php"}, &requests, &received
}

// A server may cut such a body off once it gets too large for it, with the
// status for that. The stream had been sending until then, so what it sent
// is a measurement, and the next request follows.
func TestUploadCountsAStreamThatTheServerCuts(t *testing.T) {
	clientTimeout(t, 5*time.Second)
	s, requests, received := cuttingServer(t, http.StatusRequestEntityTooLarge)

	_, total, err := s.Upload(true, true, false, false, 2, 16, 1500*time.Millisecond)

	if err != nil {
		t.Errorf("Upload returned error: %v", err)
	}
	if got := uint64(received.Load()); got == 0 || got < total/2 {
		t.Errorf("counted %d bytes while the server received %d", total, got)
	}
	if n := requests.Load(); n <= 2 {
		t.Errorf("%d requests, want the cut ones replaced", n)
	}
}

// Any other error status is a refusal for a stream as well, whether it
// comes after a while or at once.
func TestUploadFailsWhenAStreamIsRefusedLate(t *testing.T) {
	clientTimeout(t, 5*time.Second)
	s, _, _ := cuttingServer(t, http.StatusInternalServerError)

	_, total, err := s.Upload(true, true, false, false, 2, 16, 1500*time.Millisecond)

	if err == nil || err.Error() != "the upload request returned HTTP 500 Internal Server Error" {
		t.Errorf("error = %v, want the refusal reported", err)
	}
	if total != 0 {
		t.Errorf("counted %d bytes", total)
	}
}

// The status for a body that got too large shows that the server read the
// stream, also when it comes at once.
func TestUploadCountsAStreamThatTheServerCutsAtOnce(t *testing.T) {
	var received atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.CopyN(io.Discard, r.Body, 4<<20)
		received.Add(n)
		w.WriteHeader(http.StatusRequestEntityTooLarge)
	}))

	s := &Server{Server: ts.URL, UploadURL: "empty.php"}
	_, total, err := s.Upload(true, true, false, false, 1, 16, time.Second)
	ts.Close()

	if err != nil {
		t.Errorf("Upload returned error: %v", err)
	}
	if got := uint64(received.Load()); total == 0 || got < total/2 {
		t.Errorf("counted %d bytes while the server received %d", total, got)
	}
}

func TestUploadFailsWhenAStreamIsRefusedAtOnce(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	s := &Server{Server: ts.URL, UploadURL: "empty.php"}
	_, total, err := s.Upload(true, true, false, false, 2, 16, time.Second)

	if err == nil || err.Error() != "the upload request returned HTTP 404 Not Found" {
		t.Errorf("error = %v, want the refusal reported", err)
	}
	if total != 0 {
		t.Errorf("counted %d bytes", total)
	}
}

// One dropped connection does not spoil such an upload. No request of it is
// ever accepted, and the others had been sending all along.
func TestUploadCountsWhatWasSentBesideADroppedRequest(t *testing.T) {
	clientTimeout(t, 5*time.Second)
	var requests atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 2 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			conn.(*net.TCPConn).SetLinger(0)
			conn.Close()
			return
		}
		io.Copy(io.Discard, r.Body)
	}))

	s := &Server{Server: ts.URL, UploadURL: "empty.php"}
	_, total, err := s.Upload(true, true, false, false, 2, 16, 1500*time.Millisecond)
	ts.Close()

	if err != nil {
		t.Errorf("Upload returned error: %v", err)
	}
	if total == 0 {
		t.Error("nothing was counted")
	}
	if n := requests.Load(); n < 3 {
		t.Errorf("%d requests, want the dropped one replaced", n)
	}
}

// Neither does a connection that is dropped after it had carried data for a
// while, even when nothing gets through after it. A server that ends a long
// body this way did take what was sent before.
func TestUploadCountsARequestThatWasDroppedWhileSending(t *testing.T) {
	clientTimeout(t, 5*time.Second)
	var requests atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			// taken for longer than a connection dropped at once lives,
			// and dropped well before the test ends
			buf := make([]byte, 64<<10)
			for end := time.Now().Add(1200 * time.Millisecond); time.Now().Before(end); {
				if _, err := r.Body.Read(buf); err != nil {
					break
				}
			}
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		conn.(*net.TCPConn).SetLinger(0)
		conn.Close()
	}))

	s := &Server{Server: ts.URL, UploadURL: "empty.php"}
	_, total, err := s.Upload(true, true, false, false, 1, 16, 2*time.Second)
	ts.Close()

	if err != nil {
		t.Errorf("Upload returned error: %v", err)
	}
	if total == 0 {
		t.Error("nothing was counted")
	}
	if n := requests.Load(); n < 2 {
		t.Errorf("%d requests, want the dropped one replaced", n)
	}
}

// A refusal stays a refusal however long the request took to get it.
func TestUploadFailsWhenEveryRequestIsRefusedLate(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		time.Sleep(1200 * time.Millisecond)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	// the test ends while a request has been waiting for a second
	s := &Server{Server: ts.URL, UploadURL: "empty.php"}
	_, total, err := s.Upload(false, true, false, false, 1, 16, 2300*time.Millisecond)

	if err == nil || err.Error() != "the upload request returned HTTP 500 Internal Server Error" {
		t.Errorf("error = %v, want the refusal reported", err)
	}
	if total != 0 {
		t.Errorf("counted %d bytes", total)
	}
}

// What a dropped request had sent is not part of the result. Dropped
// requests are replaced several times a second, and each one would add what
// fit into the buffers on the way.
func TestUploadDoesNotCountDroppedRequests(t *testing.T) {
	const payload = 16 << 10
	var requests, accepted atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1)%2 == 0 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			conn.(*net.TCPConn).SetLinger(0)
			conn.Close()
			return
		}
		io.Copy(io.Discard, r.Body)
		// slow enough for the dropped requests to weigh in the total
		time.Sleep(100 * time.Millisecond)
		accepted.Add(1)
	}))

	s := &Server{Server: ts.URL, UploadURL: "empty.php"}
	_, total, err := s.Upload(false, true, false, false, 2, payload>>10, 2*time.Second)
	ts.Close()

	if err != nil {
		t.Errorf("Upload returned error: %v", err)
	}
	// the requests that were cut off by the end of the test count as well
	if most := uint64(accepted.Load()+2) * payload; total == 0 || total > most {
		t.Errorf("counted %d bytes, want no more than the %d of the accepted requests", total, most)
	}
	if n := requests.Load() - accepted.Load(); n < 3 {
		t.Errorf("%d dropped requests, too few to tell", n)
	}
}

// An upload is not sent on to where a redirect points. The client would
// follow a 301, 302 or 303 as a GET without the payload, and the answer to
// that GET is no upload. A redirect is a refusal like any other answer that
// is not a success.
func TestUploadDoesNotFollowARedirect(t *testing.T) {
	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var followed atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				followed.Add(1)
			}))
			defer target.Close()
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				http.Redirect(w, r, target.URL+r.URL.RequestURI(), status)
			}))
			defer ts.Close()

			s := &Server{Server: ts.URL, UploadURL: "empty.php"}
			_, total, err := s.Upload(false, true, false, false, 1, 16, time.Second)

			want := fmt.Sprintf("the upload request returned HTTP %d %s", status, http.StatusText(status))
			if err == nil || err.Error() != want {
				t.Errorf("error = %v, want %q", err, want)
			}
			if total != 0 {
				t.Errorf("counted %d bytes", total)
			}
			if n := followed.Load(); n != 0 {
				t.Errorf("%d requests went on to the redirect target, want none", n)
			}
		})
	}
}
