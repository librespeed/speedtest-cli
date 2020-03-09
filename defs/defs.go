package defs

const (
	// chunks to download in download test
	downloadChunks = 100
	// payload size per upload request
	uploadSize = 1024 * 1024
)

var (
	// values to be filled in by build script
	BuildDate   string
	ProgName    string
	ProgVersion string
	UserAgent   = ProgName + "/" + ProgVersion
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
