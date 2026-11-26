package speedtest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/librespeed/speedtest-cli/defs"
)

func TestApproximateLocationSummary(t *testing.T) {
	location := &defs.LocationInfo{
		City:        "Boydton",
		Region:      "Virginia",
		Country:     "US",
		CountryName: "United States",
		Latitude:    36.6676,
		Longitude:   -78.3875,
	}

	if got, want := approximateLocationSummary(location), "Boydton, Virginia, United States (36.6676, -78.3875)"; got != want {
		t.Errorf("approximateLocationSummary = %q, want %q", got, want)
	}
}

func TestApproximateLocationSummaryMalformedCoordinates(t *testing.T) {
	location := &defs.LocationInfo{
		City:      "Boydton",
		Region:    "Virginia",
		Country:   "US",
		Latitude:  91,
		Longitude: -181,
	}

	if got, want := approximateLocationSummary(location), "Boydton, Virginia, US"; got != want {
		t.Errorf("approximateLocationSummary = %q, want %q", got, want)
	}
}

func TestApproximateLocationSummarySingleValidCoordinate(t *testing.T) {
	location := &defs.LocationInfo{
		City:     "Boydton",
		Latitude: 36.6676,
	}

	if got, want := approximateLocationSummary(location), "Boydton (Latitude 36.6676)"; got != want {
		t.Errorf("approximateLocationSummary = %q, want %q", got, want)
	}
}

func TestApproximateLocationSummaryCleansWhitespaceAndInvisibleText(t *testing.T) {
	location := &defs.LocationInfo{
		City:        "   ",
		Region:      "\u200b",
		Country:     "US",
		CountryName: "\n",
	}

	if got, want := approximateLocationSummary(location), "US"; got != want {
		t.Errorf("approximateLocationSummary = %q, want %q", got, want)
	}
}

func TestSendTelemetryPreservesLocationMetadata(t *testing.T) {
	ispInfoPayload := make(chan string, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("ParseMultipartForm: %v", err)
			http.Error(w, "bad multipart", http.StatusBadRequest)
			return
		}
		ispInfoPayload <- r.FormValue("ispinfo")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("1 abc"))
	}))
	t.Cleanup(ts.Close)

	oldClient := telemetryClient
	telemetryClient = ts.Client()
	t.Cleanup(func() { telemetryClient = oldClient })

	telemetryServer := defs.TelemetryServer{
		Level:  defs.TelemetryLevelFull,
		Server: ts.URL,
		Path:   "telemetry",
		Share:  "share",
	}
	ispInfo := &defs.GetIPResult{
		ProcessedString: "128.94.2.134 - AS8075",
		RawISPInfo:      []byte(`{"ip":"128.94.2.134","city":"Boydton","org":"Microsoft"}`),
		Location: &defs.LocationInfo{
			City:        "Boydton",
			Region:      "Virginia",
			Country:     "US",
			CountryName: "United States",
			Latitude:    36.6676,
			Longitude:   -78.3875,
		},
	}

	link, err := sendTelemetry(telemetryServer, ispInfo, 100, 50, 10, 1, "", defs.TelemetryExtra{})
	if err != nil {
		t.Fatalf("sendTelemetry: %v", err)
	}
	if !strings.Contains(link, "id=abc") {
		t.Errorf("share link = %q, want id=abc", link)
	}

	payload := <-ispInfoPayload
	for _, want := range []string{
		`"processedString":"128.94.2.134 - AS8075"`,
		`"rawIspInfo":{"ip":"128.94.2.134","city":"Boydton","org":"Microsoft"}`,
		`"location":{"latitude":36.6676,"longitude":-78.3875,"city":"Boydton","region":"Virginia","country":"US","countryName":"United States"}`,
	} {
		if !strings.Contains(payload, want) {
			t.Errorf("telemetry ispinfo lacks %s: %s", want, payload)
		}
	}
}

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

// The workers can all be done while their results still wait in the channel.
// Those results count like any other: one of them may be the fastest server.
func TestCollectPingsKeepsTheResultsOfWorkersThatAreDone(t *testing.T) {
	// far more than a real list has, so that collecting them takes long
	// enough for the "all done" signal to arrive in the middle
	const servers = 5000

	for round := 0; round < 20; round++ {
		var wg sync.WaitGroup
		results := make(chan PingResult, servers)
		for i := 0; i < servers; i++ {
			wg.Add(1)
			results <- PingResult{Index: i, Ping: float64(i + 1)}
			wg.Done()
		}

		if got := len(collectPings(results, &wg)); got != servers {
			t.Fatalf("collected %d of %d results", got, servers)
		}
	}
}
