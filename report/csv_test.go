package report

import (
	"strings"
	"testing"

	"github.com/gocarina/gocsv"
)

func TestCSVLocationHeadersAndValues(t *testing.T) {
	reports := []CSVReport{{
		City:        "Boydton",
		Region:      "Virginia",
		Country:     "US",
		CountryName: "United States",
		Latitude:    "36.6676",
		Longitude:   "-78.3875",
	}}

	b, err := gocsv.MarshalBytes(&reports)
	if err != nil {
		t.Fatalf("MarshalBytes: %v", err)
	}
	csvText := string(b)
	for _, header := range []string{"City", "Region", "Country", "Country Name", "Latitude", "Longitude"} {
		if !strings.Contains(csvText, header) {
			t.Errorf("CSV output lacks %q: %s", header, csvText)
		}
	}
	for _, value := range []string{"Boydton", "Virginia", "US", "United States", "36.6676", "-78.3875"} {
		if !strings.Contains(csvText, value) {
			t.Errorf("CSV output lacks %q: %s", value, csvText)
		}
	}
}
