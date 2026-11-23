package main

import (
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
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

	// The largest size passes. Its server takes the connection and never
	// answers, so the run ends at the probe and does not get to an upload
	// of 2 GiB. The listener stays open, so nothing else can get its port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	silent := writeServerList(t, listedServer{1, "Silent", "http://" + ln.Addr().String(), "empty.php"})
	r := runCLI(t, "--local-json", silent, "--server", "1", "--duration", "1", "--timeout", "1", "--no-icmp", "--no-download", "--json", "--upload-size", "2097151")
	if msg := r.failure(t); r.code != 1 || !strings.Contains(msg, "is not responding") {
		t.Errorf("with the largest size: exit status %d and error %q, want 1 and the silent server reported", r.code, msg)
	}

	// a run that does not upload, or allocates no payload, does not care
	for _, unused := range []string{"--no-upload", "--no-pre-allocate"} {
		if r := runCLI(t, append(args, unused, "--upload-size", "0")...); r.code != 0 {
			t.Errorf("with %s: exit status %d, want 0, stderr:\n%s", unused, r.code, r.stderr)
		}
	}

	// and neither does one that only lists the servers
	r = runCLI(t, "--local-json", list, "--list", "--upload-size", "0")
	if r.code != 0 || !strings.Contains(r.stdout, "1: Up") {
		t.Errorf("with --list: exit status %d and stdout %q, want 0 and the server listed, stderr:\n%s", r.code, r.stdout, r.stderr)
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

// A file with a certificate in it is the trust store, whatever else it
// holds: a block that is no certificate next to it does not get the file
// refused, and the run trusts the server the certificate is for.
func TestCACertFileWithACertificateIsUsed(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/getIP.php" {
			w.Write([]byte(`{"processedString":"127.0.0.1 - localhost","rawIspInfo":""}`))
		}
	}))
	t.Cleanup(ts.Close)
	list := writeServerList(t, listedServer{1, "Up", ts.URL, "empty.php"})

	notACertificate := "-----BEGIN CERTIFICATE-----\nbm90IGEgY2VydGlmaWNhdGU=\n-----END CERTIFICATE-----\n"
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw})
	bundle := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(bundle, append([]byte(notACertificate), certificate...), 0o644); err != nil {
		t.Fatalf("write the bundle: %v", err)
	}

	args := []string{"--local-json", list, "--server", "1", "--duration", "1", "--no-icmp", "--no-download", "--no-upload", "--json"}

	// without the file the server's certificate comes from an unknown authority
	if r := runCLI(t, args...); r.code != 1 || !strings.Contains(r.failure(t), "certificate") {
		t.Errorf("without --ca-cert: exit status %d, want 1 for the certificate, stderr:\n%s", r.code, r.stderr)
	}
	if r := runCLI(t, append(args, "--ca-cert", bundle)...); r.code != 0 {
		t.Errorf("with --ca-cert: exit status %d, want 0, stderr:\n%s", r.code, r.stderr)
	}
}
