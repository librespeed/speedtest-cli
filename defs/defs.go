package defs

import (
	"encoding/json"
	"net"
	"strings"
)

var (
	// values to be filled in by build script
	BuildDate   string
	ProgName    string
	ProgVersion string
	// UserAgent names the platform as well as the program; see buildUserAgent.
	UserAgent = buildUserAgent()
)

// GetIPResults represents the returned JSON from backend server's getIP.php endpoint
type GetIPResult struct {
	ProcessedString string          `json:"processedString"`
	RawISPInfo      json.RawMessage `json:"rawIspInfo"`
}

// IPInfoResponse represents the returned JSON from IPInfo.io's API
type IPInfoResponse struct {
	IP           string `json:"ip"`
	Hostname     string `json:"hostname"`
	City         string `json:"city"`
	Region       string `json:"region"`
	Country      string `json:"country"`
	Location     string `json:"loc"`
	Organization string `json:"org"`
	Postal       string `json:"postal"`
	Timezone     string `json:"timezone"`
	Readme       string `json:"readme,omitempty"`
}

// IP returns the client IP address, preferring rawIspInfo and falling
// back to the first token of processedString when it is a valid IP.
func (g *GetIPResult) IP() string {
	var info IPInfoResponse
	if len(g.RawISPInfo) > 0 {
		json.Unmarshal(g.RawISPInfo, &info)
	}
	if info.IP != "" {
		return info.IP
	}

	fields := strings.Fields(g.ProcessedString)
	if len(fields) > 0 && net.ParseIP(fields[0]) != nil {
		return fields[0]
	}
	return ""
}
