package main

import (
	"strings"
	"testing"
)

// Options that contradict each other are a usage error. Otherwise one of
// them wins without a word, and --simple prints its lines into the JSON, the
// CSV or the event stream.
func TestContradictoryOptionsAreRejected(t *testing.T) {
	backend := mockBackend(t)
	list := writeServerList(t, listedServer{1, "Up", backend.URL, "empty.php"})

	cases := []struct {
		options []string
		want    string
	}{
		{[]string{"--ipv4", "--ipv6"}, "incompatible options 'ipv4' and 'ipv6'"},
		{[]string{"--secure", "--insecure"}, "incompatible options 'secure' and 'insecure'"},
		{[]string{"--simple", "--json"}, "incompatible options 'simple' and 'json'"},
		{[]string{"--simple", "--csv"}, "incompatible options 'simple' and 'csv'"},
		{[]string{"--simple", "--json-stream"}, "incompatible options 'simple' and 'json-stream'"},
		// the short forms name the same options
		{[]string{"-4", "-6"}, "incompatible options 'ipv4' and 'ipv6'"},
		// of several pairs one is named
		{[]string{"--ipv4", "--ipv6", "--secure", "--insecure"}, "incompatible options 'ipv4' and 'ipv6'"},
		// these two were rejected before, in the same words
		{[]string{"--json-stream", "--json"}, "incompatible options 'json-stream' and 'json'"},
		{[]string{"--json-stream", "--csv"}, "incompatible options 'json-stream' and 'csv'"},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.options, " "), func(t *testing.T) {
			args := append([]string{"--local-json", list, "--server", "1", "--duration", "1", "--no-icmp", "--no-download", "--no-upload"}, c.options...)
			r := runCLI(t, args...)

			if r.code != 1 {
				t.Errorf("exit status %d, want 1, stderr:\n%s", r.code, r.stderr)
			}
			if r.stdout != "" {
				t.Errorf("stdout = %q, want nothing", r.stdout)
			}
			if msg := r.failure(t); msg != c.want {
				t.Errorf("error = %q, want %q", msg, c.want)
			}
		})
	}
}
