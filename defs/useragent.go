package defs

import (
	"os"
	"runtime"
	"strings"
)

// buildUserAgent reports the program, its version, the platform, and on Linux
// the distribution ID. The Rust client sends the same shape.
func buildUserAgent() string {
	platform := runtime.GOOS + "; " + runtime.GOARCH

	if id := distributionID(); id != "" {
		platform += "; " + id
	}

	return ProgName + "/" + ProgVersion + " (" + platform + ")"
}

// distributionID returns the ID from os-release, e.g. "openwrt", or "" where
// neither of its two standard locations exists.
func distributionID() string {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		if b, err = os.ReadFile("/usr/lib/os-release"); err != nil {
			return ""
		}
	}
	return parseOsReleaseID(string(b))
}

// parseOsReleaseID extracts ID from os-release content, sanitised and capped:
// it ends up in a header built from a root-writable file.
func parseOsReleaseID(content string) string {
	for _, line := range strings.Split(content, "\n") {
		bare, ok := strings.CutPrefix(line, "ID=")
		if !ok {
			continue
		}
		bare = strings.Trim(strings.TrimSpace(bare), `"'`)

		var b strings.Builder
		for _, r := range strings.ToLower(bare) {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') ||
				r == '.' || r == '_' || r == '-' {
				b.WriteRune(r)
			}
			if b.Len() >= 24 {
				break
			}
		}

		if b.Len() > 0 {
			return b.String()
		}
	}

	return ""
}
