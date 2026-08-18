package output

import (
	"encoding/json"
	"sync"
)

// --- WriteEvent: NDJSON event stream on stdout (--json-stream) ---
//
// One JSON object per line: a phase event as each stage starts, a progress
// event a second while a transfer runs, and a final result event carrying
// the same reports --json prints. The stream shares stdout with nothing --
// stream mode implies quiet, and everything human-readable is on stderr.

var (
	streamMu sync.Mutex // one event per line, even with a ticker goroutine writing
	stream   bool
)

// SetStream enables the NDJSON event stream. Called when --json-stream is set.
func SetStream(v bool) { stream = v }

// StreamEnabled reports whether the NDJSON event stream is enabled.
func StreamEnabled() bool { return stream }

// PhaseEvent announces that a test stage is starting.
type PhaseEvent struct {
	Event string `json:"event"`
	Phase string `json:"phase"`
}

// ProgressEvent reports the rate measured so far in the current stage.
// Progress is percent of the stage's configured duration that has elapsed:
// a speed test is bounded by time, not by volume, so the byte count -- the
// thing being measured -- cannot say how much is left, but elapsed over
// duration can.
type ProgressEvent struct {
	Event    string  `json:"event"`
	Phase    string  `json:"phase"`
	Seconds  float64 `json:"seconds"`
	Mbps     float64 `json:"mbps"`
	Progress int     `json:"progress"`
}

// ResultEvent terminates the stream with the reports the run produced.
type ResultEvent struct {
	Event   string      `json:"event"`
	Reports interface{} `json:"reports"`
}

// WriteEvent writes one event as a single NDJSON line to stdout.
// No-op unless the stream is enabled.
func WriteEvent(v interface{}) {
	if !stream {
		return
	}

	b, err := json.Marshal(v)
	if err != nil {
		WriteError("Error generating stream event: %s\n", err)
		return
	}

	streamMu.Lock()
	defer streamMu.Unlock()
	Default.out.Write(append(b, '\n'))
}
