package report

import (
	"time"
)

// CSVReport represents the output data fields in a CSV file
type CSVReport struct {
	Timestamp time.Time `csv:"Timestamp"`
	Name      string    `csv:"Server Name"`
	Address   string    `csv:"Address"`
	Ping      float64   `csv:"Ping"`
	Jitter    float64   `csv:"Jitter"`
	Download  float64   `csv:"Download"`
	Upload    float64   `csv:"Upload"`
	Share     string    `csv:"Share"`
	IP        string    `csv:"IP"`
	// Location columns are appended after IP to preserve existing CSV positions.
	City        string `csv:"City"`
	Region      string `csv:"Region"`
	Country     string `csv:"Country"`
	CountryName string `csv:"Country Name"`
	Latitude    string `csv:"Latitude"`
	Longitude   string `csv:"Longitude"`
}
