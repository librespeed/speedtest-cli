// Package output provides clean stdout/stderr separation for program output.
//
// Design:
//   - stdout: machine-readable data for piping (JSON, CSV, --simple, --list, --version)
//   - stderr: human-readable UI (progress, errors, debugging)
//   - quiet mode: suppresses informational UI output (for --csv, --json, --simple)
//   - debug mode: enables verbose debug output (for --debug)
package output

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// Default is the package-level output writer used by all package functions.
var Default = New(false, false)

// Writer manages output routing for the program.
type Writer struct {
	out   io.Writer // stdout: machine-readable data
	ui    io.Writer // stderr: human-readable UI
	debug bool      // enable debug output
	quiet bool      // suppress informational UI output
}

// New creates a new Writer with the given settings.
func New(debug, quiet bool) *Writer {
	return &Writer{
		out:   os.Stdout,
		ui:    os.Stderr,
		debug: debug,
		quiet: quiet,
	}
}

// SetDebug enables or disables debug output. Called when --debug is set.
func SetDebug(v bool) { Default.debug = v }

// SetQuiet enables or disables quiet mode (suppresses WriteUI).
// Called when --csv, --json, or --simple is set.
func SetQuiet(v bool) { Default.quiet = v }

// IsDebug reports whether debug output is enabled.
func IsDebug() bool { return Default.debug }

// IsQuiet reports whether informational UI output is suppressed.
func IsQuiet() bool { return Default.quiet }

// --- WriteOut: stdout (data output) ---

// WriteOut writes formatted data to stdout.
// Used for JSON, CSV, --simple results, --list, and --version.
// Does NOT append newline; caller controls formatting.
func WriteOut(format string, args ...interface{}) { Default.WriteOut(format, args...) }

func (w *Writer) WriteOut(format string, args ...interface{}) {
	fmt.Fprintf(w.out, format, args...)
}

// --- WriteUI: stderr (informational, suppressed when quiet) ---

// WriteUI writes informational messages to stderr.
// Suppressed when quiet=true (--csv, --json, --simple modes).
// Does NOT append newline; caller controls formatting.
func WriteUI(format string, args ...interface{}) { Default.WriteUI(format, args...) }

func (w *Writer) WriteUI(format string, args ...interface{}) {
	if w.quiet {
		return
	}
	fmt.Fprintf(w.ui, format, args...)
}

// WriteUIBlank writes a blank line to stderr.
// Unlike WriteUI, this is NOT suppressed in quiet mode,
// because multi-server results and --list mode need spacing.
func WriteUIBlank() { Default.WriteUIBlank() }

func (w *Writer) WriteUIBlank() {
	fmt.Fprintln(w.ui)
}

// --- WriteDebug: stderr (debugging, only when --debug is set) ---

// WriteDebug writes debug messages to stderr.
// Only shown when debug=true (--debug flag is set).
// Does NOT append newline; caller controls formatting.
func WriteDebug(format string, args ...interface{}) { Default.WriteDebug(format, args...) }

func (w *Writer) WriteDebug(format string, args ...interface{}) {
	if !w.debug {
		return
	}
	fmt.Fprintf(w.ui, format, args...)
}

// --- WriteError: stderr (always shown) ---

// WriteError writes error messages to stderr.
// Always shown regardless of mode.
// Does NOT append newline; caller controls formatting.
func WriteError(format string, args ...interface{}) { Default.WriteError(format, args...) }

func (w *Writer) WriteError(format string, args ...interface{}) {
	fmt.Fprintf(w.ui, format, args...)
}

// --- Fatal: stderr + exit ---

// Fatal writes args to stderr with a trailing newline and exits with code 1.
// Supports: Fatal(err), Fatal("message"), Fatal("msg", arg).
func Fatal(args ...interface{}) { Default.Fatal(args...) }

func (w *Writer) Fatal(args ...interface{}) {
	fmt.Fprint(w.ui, args...)
	fmt.Fprintln(w.ui)
	os.Exit(1)
}

// Fatalf writes a formatted message to stderr with a trailing newline
// and exits with code 1.
func Fatalf(format string, args ...interface{}) { Default.Fatalf(format, args...) }

func (w *Writer) Fatalf(format string, args ...interface{}) {
	fmt.Fprintf(w.ui, format, args...)
	fmt.Fprintln(w.ui)
	os.Exit(1)
}

// --- Sanitize: make server-supplied strings safe to display ---

// Sanitize strips control characters from a string so it can be printed
// without letting a remote party drive the terminal.
//
// Server names, sponsor strings and the getIP response all come off the wire
// (over plain HTTP for schemeless servers), so they are attacker-influenced.
// Left raw, an embedded ESC sequence can rewrite earlier lines, hide text or
// recolour the output, and an embedded newline can forge an extra entry in
// --list output that a script would then parse as real.
//
// Dropped: C0 controls (including ESC, CR, LF and TAB), DEL, and C1 controls
// (0x80-0x9F, where 0x9B doubles as CSI on some terminals). Printable Unicode
// is left alone.
func Sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return -1
		}
		return r
	}, s)
}
