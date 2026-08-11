package defs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// getIPHandler serves a canned getIP.php response and records the parsed result.
func getIPResultFromPayload(t *testing.T, payload string) *GetIPResult {
	t.Helper()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(payload))
	}))
	defer ts.Close()

	s := &Server{Server: ts.URL, GetIPURL: "/"}
	got, err := s.GetIPInfo("km")
	if err != nil {
		t.Fatalf("GetIPInfo returned error: %v", err)
	}
	return got
}

func TestGetIPInfoParsesObject(t *testing.T) {
	got := getIPResultFromPayload(t, `{"processedString":"1.2.3.4","rawIspInfo":{"ip":"1.2.3.4","city":"Prague","org":"ACME"}}`)

	if got.ProcessedString != "1.2.3.4" {
		t.Errorf("ProcessedString = %q, want %q", got.ProcessedString, "1.2.3.4")
	}
	if got.IP() != "1.2.3.4" {
		t.Errorf("IP() = %q, want %q", got.IP(), "1.2.3.4")
	}
}

// The rawIspInfo field comes back as an empty string from some backends
// (issue #85). The result must keep it as-is so telemetry reports an empty
// string rather than an all-empty object, and must not lose processedString.
func TestGetIPInfoKeepsEmptyRawIspInfo(t *testing.T) {
	got := getIPResultFromPayload(t, `{"processedString":"10.0.0.70","rawIspInfo":""}`)

	if got.ProcessedString != "10.0.0.70" {
		t.Errorf("ProcessedString = %q, want %q", got.ProcessedString, "10.0.0.70")
	}
	if got.IP() != "" {
		t.Errorf("IP() = %q, want empty", got.IP())
	}

	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"processedString":"10.0.0.70","rawIspInfo":""}`
	if string(b) != want {
		t.Errorf("Marshal(GetIPResult) = %s, want %s", b, want)
	}
}

// Some backends wrap the whole payload in processedString (issue #78). The
// object inside must still surface the client address.
func TestGetIPInfoExtractsIPFromNestedPayload(t *testing.T) {
	got := getIPResultFromPayload(t, `{"processedString":"{\"ip\":\"9.9.9.9\",\"city\":\"X\"}","rawIspInfo":{"ip":"9.9.9.9","city":"X"}}`)

	if got.IP() != "9.9.9.9" {
		t.Errorf("IP() = %q, want %q", got.IP(), "9.9.9.9")
	}
}

func TestGetIPInfoAbsentRawIspInfo(t *testing.T) {
	got := getIPResultFromPayload(t, `{"processedString":"1.2.3.4"}`)

	if got.ProcessedString != "1.2.3.4" {
		t.Errorf("ProcessedString = %q, want %q", got.ProcessedString, "1.2.3.4")
	}
	if got.IP() != "" {
		t.Errorf("IP() = %q, want empty", got.IP())
	}
}
