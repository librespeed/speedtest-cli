package defs

import "runtime"

var (
	// values to be filled in by build script
	BuildDate   string
	ProgName    string
	ProgVersion string
	// UserAgent names the platform as well as the program, the way a browser
	// does. A server's telemetry already stores this header, so reporting the
	// operating system and architecture here is what lets an operator tell
	// which kinds of machine are measuring against them without any change to
	// what the client sends. It stays coarse deliberately: the kernel version
	// or the hostname would identify the machine rather than describe it.
	UserAgent = ProgName + "/" + ProgVersion + " (" + runtime.GOOS + "; " + runtime.GOARCH + ")"
)

// GetIPResults represents the returned JSON from backend server's getIP.php endpoint
type GetIPResult struct {
	ProcessedString string         `json:"processedString"`
	RawISPInfo      IPInfoResponse `json:"rawIspInfo"`
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
