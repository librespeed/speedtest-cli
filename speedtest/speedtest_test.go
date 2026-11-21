package speedtest

import (
	"sync"
	"testing"
)

func TestWellKnownServerURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"default list URL gets the path at the root",
			"https://librespeed.org/backend-servers/servers.php",
			"https://librespeed.org/.well-known/librespeed",
		},
		{
			"custom server-json URL",
			"https://speedtest.example.com/servers.json",
			"https://speedtest.example.com/.well-known/librespeed",
		},
		{
			"http scheme is kept",
			"http://example.com/list",
			"http://example.com/.well-known/librespeed",
		},
		{
			"host with port",
			"https://example.com:8443/servers.php",
			"https://example.com:8443/.well-known/librespeed",
		},
		{
			"query string is dropped",
			"https://example.com/servers.php?x=1",
			"https://example.com/.well-known/librespeed",
		},
		{
			"unparseable URL falls back to appending",
			"not a url",
			"not a url/.well-known/librespeed",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := wellKnownServerURL(c.in); got != c.want {
				t.Errorf("wellKnownServerURL(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// The workers can all be done while their results still wait in the channel.
// Those results count like any other: one of them may be the fastest server.
func TestCollectPingsKeepsTheResultsOfWorkersThatAreDone(t *testing.T) {
	// far more than a real list has, so that collecting them takes long
	// enough for the "all done" signal to arrive in the middle
	const servers = 5000

	for round := 0; round < 20; round++ {
		var wg sync.WaitGroup
		results := make(chan PingResult, servers)
		for i := 0; i < servers; i++ {
			wg.Add(1)
			results <- PingResult{Index: i, Ping: float64(i + 1)}
			wg.Done()
		}

		if got := len(collectPings(results, &wg)); got != servers {
			t.Fatalf("collected %d of %d results", got, servers)
		}
	}
}
