package defs

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The transfer tests below cover the timing semantics introduced by wg.Wait():
// Download and Upload now return only once every in-flight request has
// unwound. Against a server that never ends a request on its own, the only
// thing that can end them is the context cancellation, so these fail (by
// timing out) if wg.Wait() can hang.

const (
	testRequests = 2
	// long enough that the requests are still in flight when the test ends
	testDuration = 500 * time.Millisecond
	// The spawn loop sleeps 200ms per request before the duration timer even
	// starts, so a healthy run is ~900ms; measured unwind after cancel() is
	// 10-40ms, with or without -race. The rest is slack for a loaded runner --
	// kept tight so a hanging wg.Wait() fails fast instead of stalling CI.
	returnBudget = testRequests*200*time.Millisecond + testDuration + 2*time.Second
)

// hangingDownloadServer streams forever until the client goes away.
func hangingDownloadServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := make([]byte, 32*1024)
		for {
			select {
			case <-r.Context().Done():
				return
			default:
			}
			if _, err := w.Write(chunk); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
}

// hangingUploadServer drains the body, then holds the request open.
func hangingUploadServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
}

func runWithinBudget(t *testing.T, name string, fn func() error) {
	t.Helper()

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- fn() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s returned error: %v", name, err)
		}
		if elapsed := time.Since(start); elapsed > returnBudget {
			t.Errorf("%s took %s, budget was %s", name, elapsed, returnBudget)
		}
	case <-time.After(returnBudget):
		t.Fatalf("%s did not return within %s: wg.Wait() is hanging on cancelled requests", name, returnBudget)
	}
}

func TestDownloadReturnsWhenServerNeverEndsTheResponse(t *testing.T) {
	ts := hangingDownloadServer(t)
	defer ts.Close()

	s := &Server{Server: ts.URL, DownloadURL: "/"}

	var total uint64
	runWithinBudget(t, "Download", func() error {
		_, n, err := s.Download(true, false, false, testRequests, 100, testDuration)
		total = n
		return err
	})

	if total == 0 {
		t.Error("Download reported 0 bytes, expected the counter to have seen traffic")
	}
}

func TestUploadReturnsWhenServerNeverResponds(t *testing.T) {
	ts := hangingUploadServer(t)
	defer ts.Close()

	s := &Server{Server: ts.URL, UploadURL: "/"}

	runWithinBudget(t, "Upload", func() error {
		_, _, err := s.Upload(false, true, false, false, testRequests, 32, testDuration)
		return err
	})
}

// The no-prealloc path streams from crypto/rand, so the request body never
// ends on its own either; only cancellation can stop it.
func TestUploadNoPreallocReturnsWhenServerNeverResponds(t *testing.T) {
	ts := hangingUploadServer(t)
	defer ts.Close()

	s := &Server{Server: ts.URL, UploadURL: "/"}

	runWithinBudget(t, "Upload(noPrealloc)", func() error {
		_, _, err := s.Upload(true, true, false, false, testRequests, 32, testDuration)
		return err
	})
}
