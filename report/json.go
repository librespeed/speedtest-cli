package report

import (
	"encoding/json"
	"time"

	"github.com/librespeed/speedtest-cli/defs"
)

// JSONReport represents the output data fields in a JSON file
type JSONReport struct {
	Timestamp     time.Time `json:"timestamp"`
	Server        Server    `json:"server"`
	Client        Client    `json:"client"`
	BytesSent     uint64    `json:"bytes_sent"`
	BytesReceived uint64    `json:"bytes_received"`
	Ping          float64   `json:"ping"`
	Jitter        float64   `json:"jitter"`
	Upload        float64   `json:"upload"`
	Download      float64   `json:"download"`
	Share         string    `json:"share"`
}

// Server represents the speed test server's information
type Server struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Client represents the speed test client's information
type Client struct {
	defs.IPInfoResponse
}

// NewClient builds a Client from the raw ISP info payload a backend returned.
// The payload may be an object, an empty string, or absent; anything that does
// not parse as an object yields an empty Client rather than an error.
func NewClient(raw json.RawMessage) Client {
	var data struct {
		defs.IPInfoResponse
		ASName string `json:"as_name"`
	}

	if len(raw) > 0 {
		json.Unmarshal(raw, &data)
	}

	c := Client{IPInfoResponse: data.IPInfoResponse}

	// Current backends use as_name, while older ones use org.
	if c.Organization == "" {
		c.Organization = data.ASName
	}

	return c
}
