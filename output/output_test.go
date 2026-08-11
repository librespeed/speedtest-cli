package output

import "testing"

func TestSanitize(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain text is untouched", "normal text", "normal text"},
		{"newline is dropped", "hello\nworld", "helloworld"},
		{"carriage return is dropped", "hello\rworld", "helloworld"},
		{"tab is dropped", "hello\tworld", "helloworld"},
		{"nul is dropped", "hello\x00world", "helloworld"},
		{"ANSI colour sequence loses its ESC", "\x1b[31mred\x1b[0m", "[31mred[0m"},
		{"OSC window title sequence loses ESC and BEL", "\x1b]0;pwned\x07", "]0;pwned"},
		{"DEL is dropped", "foo\x7fbar", "foobar"},
		{"C1 CSI is dropped", "foo\u009b31mbar", "foo31mbar"},
		{"C1 lower bound U+0080 is dropped", "foo\u0080bar", "foobar"},
		{"C1 upper bound U+009F is dropped", "foo\u009fbar", "foobar"},
		{"U+00A0 just past C1 is kept", "foo\u00a0bar", "foo\u00a0bar"},
		{"space just past C0 is kept", "foo bar", "foo bar"},
		{"non-ASCII text is kept", "Český server \U0001f1e8\U0001f1ff", "Český server \U0001f1e8\U0001f1ff"},
		{"forged --list entry is collapsed onto one line", "Server A\n999: Fake", "Server A999: Fake"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Sanitize(tt.in); got != tt.want {
				t.Errorf("Sanitize(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestSanitizeDropsEveryControlRune asserts the boundaries exhaustively rather
// than by example, so a future rewrite cannot silently let one class through.
func TestSanitizeDropsEveryControlRune(t *testing.T) {
	for r := rune(0); r <= 0x9f; r++ {
		if r >= 0x20 && r <= 0x7e {
			continue // printable ASCII
		}
		if got := Sanitize(string(r)); got != "" {
			t.Errorf("Sanitize(%U) = %q, want it to be dropped", r, got)
		}
	}
	for _, r := range []rune{0x20, 0x7e, 0xa0, 0xa1, 0x2028, 0x1f600} {
		if got := Sanitize(string(r)); got != string(r) {
			t.Errorf("Sanitize(%U) = %q, want it kept", r, got)
		}
	}
}
