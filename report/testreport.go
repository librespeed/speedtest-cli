package report

import (
	"time"

	"github.com/librespeed/speedtest-cli/defs"
)

// JSONReport represents the output data fields in a JSON file
type Report struct {
	Timestamp     time.Time `json:"timestamp"`
	Server        Server    `json:"server"`
	Client        Client    `json:"client"`
	BytesSent     int       `json:"bytes_sent"`
	BytesReceived int       `json:"bytes_received"`
	Ping          float64   `json:"ping"`
	Jitter        float64   `json:"jitter"`
	Upload        float64   `json:"upload"`
	Download      float64   `json:"download"`
	Share         string    `json:"share"`
}

type FlatReport struct {
	Timestamp time.Time `csv:"Timestamp"`
	Name      string    `csv:"Server Name"`
	Address   string    `csv:"Address"`
	Ping      float64   `csv:"Ping"`
	Jitter    float64   `csv:"Jitter"`
	Download  float64   `csv:"Download"`
	Upload    float64   `csv:"Upload"`
	Share     string    `csv:"Share"`
	IP        string    `csv:"IP"`
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

func (r Report) GetFlatReport() FlatReport {
	var rep FlatReport

	rep.Timestamp = r.Timestamp
	rep.Name = r.Server.Name
	rep.Address = r.Server.URL
	rep.Ping = r.Ping
	rep.Jitter = r.Jitter
	rep.Download = r.Download
	rep.Upload = r.Upload
	rep.Share = r.Share
	rep.IP = r.Client.IP

	return rep
}
