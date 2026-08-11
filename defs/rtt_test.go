package defs

import (
	"testing"
	"time"
)

func TestRttMillis(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want float64
	}{
		{"zero", 0, 0},
		{"whole millisecond", time.Millisecond, 1},
		{"a second", time.Second, 1000},
		// Duration.Milliseconds truncates, so each of these would come back a
		// whole millisecond short -- and the first three as no time at all.
		{"a quarter of a millisecond", 250 * time.Microsecond, 0.25},
		{"just under a millisecond", 999 * time.Microsecond, 0.999},
		{"a single microsecond", time.Microsecond, 0.001},
		{"one and a half milliseconds", 1500 * time.Microsecond, 1.5},
		{"microsecond resolution is kept", 1234 * time.Microsecond, 1.234},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := rttMillis(c.in); got != c.want {
				t.Errorf("rttMillis(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// A local link can answer well inside a millisecond, which is the case the
// truncating conversion reported as zero.
func TestRttMillisKeepsSubMillisecondApart(t *testing.T) {
	fast, slow := rttMillis(200*time.Microsecond), rttMillis(800*time.Microsecond)
	if fast >= slow {
		t.Errorf("200us reported as %v, 800us as %v; the two should differ", fast, slow)
	}
	if fast == 0 {
		t.Error("200us reported as 0 ms, which is what the truncating conversion did")
	}
}
