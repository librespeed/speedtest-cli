package speedtest

import (
	"net/http"
	"net/url"
	"testing"
)

func TestRefuseDowngrade(t *testing.T) {
	request := func(rawURL string) *http.Request {
		u, err := url.Parse(rawURL)
		if err != nil {
			t.Fatalf("parse %q: %v", rawURL, err)
		}
		return &http.Request{URL: u}
	}

	cases := []struct {
		name    string
		from    string
		to      string
		wantErr bool
	}{
		{"HTTPS to HTTPS", "https://a.example/empty.php", "https://b.example/empty.php", false},
		{"HTTPS to plain HTTP", "https://a.example/empty.php", "http://a.example/empty.php", true},
		{"plain HTTP to plain HTTP", "http://a.example/telemetry", "http://b.example/telemetry", false},
		{"plain HTTP to HTTPS", "http://a.example/telemetry", "https://a.example/telemetry", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := refuseDowngrade(request(c.to), []*http.Request{request(c.from)})
			if (err != nil) != c.wantErr {
				t.Errorf("error = %v, want an error: %t", err, c.wantErr)
			}
		})
	}

	t.Run("too many redirects", func(t *testing.T) {
		via := make([]*http.Request, 10)
		for i := range via {
			via[i] = request("https://a.example/empty.php")
		}
		if err := refuseDowngrade(request("https://a.example/empty.php"), via); err == nil {
			t.Error("followed an eleventh redirect")
		}
	})
}
