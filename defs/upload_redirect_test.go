package defs

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// uploadSeen is what a test server has seen of uploads: the bytes of payload
// it has read in POSTs, the POSTs that announced no body, and every request.
// A payload request that the end of the test cut short announces a body, even
// if none of it arrives.
type uploadSeen struct {
	payload    atomic.Int64
	emptyPOSTs atomic.Int64
	requests   atomic.Int64
}

func (u *uploadSeen) count(r *http.Request) {
	u.requests.Add(1)
	if r.Method != http.MethodPost {
		return
	}
	if r.ContentLength == 0 {
		u.emptyPOSTs.Add(1)
	}
	n, _ := io.Copy(io.Discard, r.Body)
	u.payload.Add(n)
}

// movedServer is a server whose every URL answers with the given redirect to
// the same path under /moved, and what the two places have seen.
func movedServer(t *testing.T, status int) (s *Server, listed, target *uploadSeen) {
	t.Helper()
	listed, target = new(uploadSeen), new(uploadSeen)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/moved/") {
			target.count(r)
			return
		}
		listed.count(r)
		http.Redirect(w, r, "/moved"+r.URL.Path, status)
	}))
	t.Cleanup(ts.Close)

	return &Server{Server: ts.URL, PingURL: "empty.php", UploadURL: "empty.php"}, listed, target
}

// A server that has moved gets the payload at its new place. Its probe is
// redirected, so the upload asks once, with an empty POST, where its URL is.
// Sent to the listed URL, the payload only reaches a server that redirects,
// and the HTTP client then asks the new place with a GET, or not at all.
func TestUploadFollowsItsURL(t *testing.T) {
	const (
		requests      = 2
		uploadSizeKiB = 16
	)

	for _, status := range []int{
		http.StatusMovedPermanently,
		http.StatusFound,
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect,
	} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			s, listed, target := movedServer(t, status)
			if err := s.CheckUp(); err != nil {
				t.Fatalf("CheckUp returned error: %v", err)
			}

			_, total, err := s.Upload(false, true, false, false, requests, uploadSizeKiB, 500*time.Millisecond)

			if err != nil {
				t.Fatalf("Upload returned error: %v", err)
			}
			if n := listed.payload.Load(); n != 0 {
				t.Errorf("%d bytes of payload went to the URL that redirects, want none", n)
			}
			if a, b := listed.emptyPOSTs.Load(), target.emptyPOSTs.Load(); a != 1 || b != 1 {
				t.Errorf("%d empty POSTs at the listed URL and %d at the new place, want one each", a, b)
			}
			// what is counted is what the new place has read from POSTs, but
			// for the requests that the end of the test cut short
			read := target.payload.Load()
			unread := int64(total) - read
			if read == 0 || unread < 0 || unread > requests*uploadSizeKiB*1024 {
				t.Errorf("%d bytes counted and %d read from POSTs at the new place", total, read)
			}
		})
	}
}

// A server whose probe is not redirected is asked nothing before the upload.
// It gets the requests it always got.
func TestUploadAsksNothingOfAServerThatDoesNotRedirect(t *testing.T) {
	var seen uploadSeen
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.count(r)
	}))
	t.Cleanup(ts.Close)
	s := &Server{Server: ts.URL, PingURL: "empty.php", UploadURL: "empty.php"}
	if err := s.CheckUp(); err != nil {
		t.Fatalf("CheckUp returned error: %v", err)
	}

	_, total, err := s.Upload(false, true, false, false, 2, 16, 500*time.Millisecond)

	if err != nil {
		t.Fatalf("Upload returned error: %v", err)
	}
	if total == 0 {
		t.Error("nothing was uploaded")
	}
	if n := seen.emptyPOSTs.Load(); n != 0 {
		t.Errorf("the server got %d POSTs without a body, want none", n)
	}
}

// Unless the redirects end at a place that takes the empty POST, the server
// is tested the way it was before, against the listed URL. These servers
// count as ones whose probe was redirected, so they are asked.
func TestUploadDoesNotDependOnTheEmptyPOST(t *testing.T) {
	timeout := uploadProbeTimeout
	uploadProbeTimeout = 100 * time.Millisecond
	t.Cleanup(func() { uploadProbeTimeout = timeout })

	for _, tc := range []struct {
		name   string
		answer http.HandlerFunc
	}{
		{"an error status", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadRequest) }},
		{"no answer", func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }},
		{"a redirect to nowhere", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusFound) }},
		{"a redirect to a place that takes no POST", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path != "/empty.php":
					w.WriteHeader(http.StatusMethodNotAllowed)
				case r.ContentLength == 0:
					tc.answer(w, r)
				default:
					io.Copy(io.Discard, r.Body)
				}
			}))
			t.Cleanup(ts.Close)
			s := &Server{Server: ts.URL, UploadURL: "empty.php", probeRedirected: true}

			_, total, err := s.Upload(false, true, false, false, 2, 16, 500*time.Millisecond)

			if err != nil {
				t.Fatalf("Upload returned error: %v", err)
			}
			if total == 0 {
				t.Error("nothing was uploaded")
			}
		})
	}
}

// Redirects that lead nowhere end where the HTTP client ends them itself,
// and before any payload is sent.
func TestUploadStopsFollowingALoop(t *testing.T) {
	var asked, payload atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		n, _ := io.Copy(io.Discard, r.Body)
		payload.Add(n)
		http.Redirect(w, r, r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(ts.Close)
	s := &Server{Server: ts.URL, UploadURL: "empty.php", probeRedirected: true}

	_, _, err := s.Upload(false, true, false, false, 2, 16, 500*time.Millisecond)

	if err == nil || !strings.HasSuffix(err.Error(), "stopped after 10 redirects") {
		t.Errorf("error = %v, want the limit of 10 redirects named", err)
	}
	if n := asked.Load(); n != 10 {
		t.Errorf("the URL was asked %d times, want 10", n)
	}
	if n := payload.Load(); n != 0 {
		t.Errorf("%d bytes of payload were sent into the loop, want none", n)
	}
}

// The upload stays with the listed server. A redirect to another port or
// another host name ends the test before anything is sent there, not even
// the empty POST.
func TestUploadStaysWithTheListedServer(t *testing.T) {
	const refused = "the upload URL redirects to another server"

	for _, status := range []int{http.StatusMovedPermanently, http.StatusTemporaryRedirect} {
		for _, place := range []string{"another port", "another host"} {
			t.Run(place+" "+strconv.Itoa(status), func(t *testing.T) {
				var listed, elsewhere uploadSeen
				other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					elsewhere.count(r)
				}))
				t.Cleanup(other.Close)

				var ts *httptest.Server
				ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasPrefix(r.Host, "localhost:") {
						elsewhere.count(r)
						return
					}
					listed.count(r)
					there := other.URL
					if place == "another host" {
						there = strings.Replace(ts.URL, "127.0.0.1", "localhost", 1)
					}
					http.Redirect(w, r, there+"/moved"+r.URL.Path, status)
				}))
				t.Cleanup(ts.Close)
				s := &Server{Server: ts.URL, UploadURL: "empty.php", probeRedirected: true}

				_, total, err := s.Upload(false, true, false, false, 2, 16, 500*time.Millisecond)

				if err == nil || !strings.HasSuffix(err.Error(), refused) {
					t.Errorf("error = %v, want it to end in %q", err, refused)
				}
				if total != 0 {
					t.Errorf("%d bytes counted, want none", total)
				}
				if n := elsewhere.requests.Load(); n != 0 {
					t.Errorf("%d requests went to the other server, want none", n)
				}
				if a, b := listed.emptyPOSTs.Load(), listed.payload.Load(); a != 1 || b != 0 {
					t.Errorf("%d empty POSTs and %d bytes of payload at the listed URL, want one and none", a, b)
				}
			})
		}
	}
}

// The empty POST shows only that a place answers. One that answers without
// reading gets the payload all the same, as a listed URL that does so does.
func TestUploadGoesWhereTheEmptyPOSTIsAnswered(t *testing.T) {
	var listedPayloads, payloads atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/moved/empty.php" {
			if r.ContentLength > 0 {
				listedPayloads.Add(1)
			}
			http.Redirect(w, r, "/moved/empty.php", http.StatusTemporaryRedirect)
			return
		}
		// answered without a read of the body
		if r.ContentLength > 0 {
			payloads.Add(1)
		}
	}))
	t.Cleanup(ts.Close)
	s := &Server{Server: ts.URL, UploadURL: "empty.php", probeRedirected: true}

	_, _, err := s.Upload(false, true, false, false, 2, 16, 500*time.Millisecond)

	if err != nil {
		t.Fatalf("Upload returned error: %v", err)
	}
	if n := payloads.Load(); n == 0 {
		t.Error("no payload went to the place that answered the empty POST")
	}
	if n := listedPayloads.Load(); n != 0 {
		t.Errorf("%d payloads went to the listed URL, want none", n)
	}
}

func TestSameServer(t *testing.T) {
	for _, tc := range []struct {
		listed, next string
		want         bool
	}{
		{"http://h/empty.php", "/moved/empty.php", true},
		{"http://h:8080/empty.php", "http://h:8080/moved/empty.php", true},
		{"https://h:8443/empty.php", "https://h:8443/moved/empty.php", true},
		{"http://h/empty.php", "https://h/empty.php", true},
		{"http://h:8080/empty.php", "https://h:8080/empty.php", true},
		{"http://[::1]/empty.php", "https://[::1]/empty.php", true},
		{"https://h/empty.php", "http://h/empty.php", false},
		{"http://h/empty.php", "http://h:8080/empty.php", false},
		{"http://h:8080/empty.php", "https://h:8443/empty.php", false},
		{"http://h/empty.php", "https://h:8443/empty.php", false},
		{"http://h/empty.php", "http://www.h/empty.php", false},
		{"http://h/empty.php", "http://other/empty.php", false},
		{"http://h/empty.php", "//other/empty.php", false},
		{"http://127.0.0.1/empty.php", "http://localhost/empty.php", false},
		// the same server written another way, which the transport would
		// not know as the test server
		{"http://h:80/empty.php", "https://h/empty.php", false},
		{"http://h/empty.php", "https://h:443/empty.php", false},
		{"http://H/empty.php", "http://h/empty.php", false},
	} {
		listed, err := url.Parse(tc.listed)
		if err != nil {
			t.Fatal(err)
		}
		next, err := listed.Parse(tc.next)
		if err != nil {
			t.Fatal(err)
		}
		if got := sameServer(listed, next); got != tc.want {
			t.Errorf("sameServer(%s, %s) = %v, want %v", tc.listed, tc.next, got, tc.want)
		}
	}
}

// A redirect URL is shown without what can carry a token.
func TestWithoutQuery(t *testing.T) {
	u, err := url.Parse("https://user:secret@h/empty.php?token=abc#frag")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := withoutQuery(u), "https://user:xxxxx@h/empty.php"; got != want {
		t.Errorf("withoutQuery = %q, want %q", got, want)
	}
	if u.RawQuery == "" || u.Fragment == "" {
		t.Error("withoutQuery changed the URL it was given")
	}
}
