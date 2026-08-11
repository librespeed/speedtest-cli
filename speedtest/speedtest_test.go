package speedtest

import "testing"

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
