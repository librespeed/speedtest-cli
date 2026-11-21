package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An upload size that is not positive or is beyond the limit is a usage
// error. A negative one, or one too large to allocate, used to end in a Go
// panic once the upload test started.
func TestUploadSizeOutOfRangeIsRejected(t *testing.T) {
	backend := mockBackend(t)
	list := writeServerList(t, listedServer{1, "Up", backend.URL, "empty.php"})
	args := []string{"--local-json", list, "--server", "1", "--duration", "1", "--no-icmp", "--no-download", "--json"}

	// 2097152 KiB is 2 GiB, the first size past the limit
	for _, size := range []string{"-1", "0", "2097152"} {
		t.Run("size="+size, func(t *testing.T) {
			r := runCLI(t, append(args, "--upload-size", size)...)

			if r.code != 1 {
				t.Errorf("exit status %d, want 1, stderr:\n%s", r.code, r.stderr)
			}
			if strings.Contains(r.stderr, "panic") {
				t.Errorf("the CLI panicked:\n%s", r.stderr)
			}
			if msg, want := r.failure(t), "invalid upload size setting"; msg != want {
				t.Errorf("error = %q, want %q", msg, want)
			}
		})
	}

	// a run that does not upload, or allocates no payload, does not care
	for _, unused := range []string{"--no-upload", "--no-pre-allocate"} {
		if r := runCLI(t, append(args, unused, "--upload-size", "0")...); r.code != 0 {
			t.Errorf("with %s: exit status %d, want 0, stderr:\n%s", unused, r.code, r.stderr)
		}
	}
}

// A --ca-cert file with no certificate in it leaves nothing to trust. Say so
// at once instead of failing every HTTPS request later for an unknown
// authority.
func TestCACertFileWithoutACertificateIsRejected(t *testing.T) {
	backend := mockBackend(t)
	list := writeServerList(t, listedServer{1, "Up", backend.URL, "empty.php"})

	bundle := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(bundle, []byte("not a certificate\n"), 0o644); err != nil {
		t.Fatalf("write the bundle: %v", err)
	}

	args := []string{"--local-json", list, "--server", "1", "--duration", "1", "--no-icmp", "--no-download", "--no-upload", "--json", "--ca-cert", bundle}
	r := runCLI(t, args...)

	if r.code != 1 {
		t.Errorf("exit status %d, want 1, stderr:\n%s", r.code, r.stderr)
	}
	if msg, want := r.failure(t), "no certificate found in "+bundle; msg != want {
		t.Errorf("error = %q, want %q", msg, want)
	}

	// with verification off the trust store is not used, and neither is the file
	if r := runCLI(t, append(args, "--skip-cert-verify")...); r.code != 0 {
		t.Errorf("with --skip-cert-verify: exit status %d, want 0, stderr:\n%s", r.code, r.stderr)
	}
}
