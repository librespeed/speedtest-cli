package speedtest

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gocarina/gocsv"
	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"

	"github.com/librespeed/speedtest-cli/defs"
	"github.com/librespeed/speedtest-cli/report"
)

const (
	// serverListUrl is the default remote server JSON URL
	serverListUrl = `https://librespeed.org/backend-servers/servers.php`

	defaultTelemetryLevel  = "basic"
	defaultTelemetryServer = "https://librespeed.org"
	defaultTelemetryPath   = "/results/telemetry.php"
	defaultTelemetryShare  = "/results/"
)

type PingJob struct {
	Index  int
	Server defs.Server
}

type PingResult struct {
	Index int
	Ping  float64
}

// SpeedTest is the actual main function that handles the speed test(s)
func SpeedTest(c *cli.Context) error {
	// check for suppressed output flags
	var silent bool
	if c.Bool(defs.OptionSimple) || c.Bool(defs.OptionJSON) || c.Bool(defs.OptionCSV) {
		log.SetLevel(log.WarnLevel)
		silent = true
	}

	// check for debug flag
	if c.Bool(defs.OptionDebug) {
		log.SetLevel(log.DebugLevel)
	}

	// print help
	if c.Bool(defs.OptionHelp) {
		return cli.ShowAppHelp(c)
	}

	// print version
	if c.Bool(defs.OptionVersion) {
		log.Warnf("%s %s (built on %s)", defs.ProgName, defs.ProgVersion, defs.BuildDate)
		log.Warn("https://github.com/librespeed/speedtest-cli")
		log.Warn("Licensed under GNU Lesser General Public License v3.0")
		log.Warn("LibreSpeed\tCopyright (C) 2016-2020 Federico Dossena")
		log.Warn("librespeed-cli\tCopyright (C) 2020 Maddie Zhan")
		log.Warn("librespeed.org\tCopyright (C)")
		return nil
	}

	// set CSV delimiter
	gocsv.TagSeparator = c.String(defs.OptionCSVDelimiter)

	// if --csv-header is given, print the header and exit (same behavior speedtest-cli)
	if c.Bool(defs.OptionCSVHeader) {
		var rep []report.CSVReport
		b, _ := gocsv.MarshalBytes(&rep)
		log.Warnf("%s", b)
		return nil
	}

	// read telemetry settings if --share or any --telemetry option is given
	var telemetryServer defs.TelemetryServer
	telemetryJSON := c.String(defs.OptionTelemetryJSON)
	telemetryLevel := c.String(defs.OptionTelemetryLevel)
	telemetryServerString := c.String(defs.OptionTelemetryServer)
	telemetryPath := c.String(defs.OptionTelemetryPath)
	telemetryShare := c.String(defs.OptionTelemetryShare)
	if c.Bool(defs.OptionShare) || telemetryJSON != "" || telemetryLevel != "" || telemetryServerString != "" || telemetryPath != "" || telemetryShare != "" {
		if telemetryJSON != "" {
			b, err := ioutil.ReadFile(telemetryJSON)
			if err != nil {
				log.Errorf("Cannot read %s: %s", telemetryJSON, err)
				return err
			}
			if err := json.Unmarshal(b, &telemetryServer); err != nil {
				log.Errorf("Error parsing %s: %s", err)
				return err
			}
		}

		if telemetryLevel != "" {
			if telemetryLevel != "disabled" && telemetryLevel != "basic" && telemetryLevel != "full" && telemetryLevel != "debug" {
				log.Fatalf("Unsupported telemetry level: %s", telemetryLevel)
			}
			telemetryServer.Level = telemetryLevel
		} else if telemetryServer.Level == "" {
			telemetryServer.Level = defaultTelemetryLevel
		}

		if telemetryServerString != "" {
			telemetryServer.Server = telemetryServerString
		} else if telemetryServer.Server == "" {
			telemetryServer.Server = defaultTelemetryServer
		}

		if telemetryPath != "" {
			telemetryServer.Path = telemetryPath
		} else if telemetryServer.Path == "" {
			telemetryServer.Path = defaultTelemetryPath
		}

		if telemetryShare != "" {
			telemetryServer.Share = telemetryShare
		} else if telemetryServer.Share == "" {
			telemetryServer.Share = defaultTelemetryShare
		}
	}

	if req := c.Int(defs.OptionConcurrent); req <= 0 {
		log.Errorf("Concurrent requests cannot be lower than 1: %d is given", req)
		return errors.New("invalid concurrent requests setting")
	}

	// HTTP requests timeout
	http.DefaultClient.Timeout = time.Duration(c.Int(defs.OptionTimeout)) * time.Second

	forceIPv4 := c.Bool(defs.OptionIPv4)
	forceIPv6 := c.Bool(defs.OptionIPv6)

	var network string
	switch {
	case forceIPv4:
		network = "ip4"
	case forceIPv6:
		network = "ip6"
	default:
		network = "ip"
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: c.Bool(defs.OptionSkipCertVerify)}

	// bind to source IP address if given, or if ipv4/ipv6 is forced
	if src := c.String(defs.OptionSource); src != "" || (forceIPv4 || forceIPv6) {
		var localTCPAddr *net.TCPAddr
		if src != "" {
			// first we parse the IP to see if it's valid
			addr, err := net.ResolveIPAddr(network, src)
			if err != nil {
				if strings.Contains(err.Error(), "no suitable address") {
					if forceIPv6 {
						log.Errorf("Address %s is not a valid IPv6 address", src)
					} else {
						log.Errorf("Address %s is not a valid IPv4 address", src)
					}
				} else {
					log.Errorf("Error parsing source IP: %s", err)
				}
				return err
			}

			log.Debugf("Using %s as source IP", src)
			localTCPAddr = &net.TCPAddr{IP: addr.IP}
		}

		var dialContext func(context.Context, string, string) (net.Conn, error)
		defaultDialer := &net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}

		if localTCPAddr != nil {
			defaultDialer.LocalAddr = localTCPAddr
		}

		switch {
		case forceIPv4:
			dialContext = func(ctx context.Context, network, address string) (conn net.Conn, err error) {
				return defaultDialer.DialContext(ctx, "tcp4", address)
			}
		case forceIPv6:
			dialContext = func(ctx context.Context, network, address string) (conn net.Conn, err error) {
				return defaultDialer.DialContext(ctx, "tcp6", address)
			}
		default:
			dialContext = defaultDialer.DialContext
		}

		// set default HTTP client's Transport to the one that binds the source address
		// this is modified from http.DefaultTransport
		transport.DialContext = dialContext
	}

	http.DefaultClient.Transport = transport

	// load server list
	var servers []defs.Server
	var err error
	if str := c.String(defs.OptionLocalJSON); str != "" {
		switch str {
		case "-":
			// load server list from stdin
			log.Info("Using local JSON server list from stdin")
			servers, err = getLocalServersReader(c.Bool(defs.OptionSecure), os.Stdin, c.IntSlice(defs.OptionExclude), c.IntSlice(defs.OptionServer), !c.Bool(defs.OptionList))
		default:
			// load server list from local JSON file
			log.Infof("Using local JSON server list: %s", str)
			servers, err = getLocalServers(c.Bool(defs.OptionSecure), str, c.IntSlice(defs.OptionExclude), c.IntSlice(defs.OptionServer), !c.Bool(defs.OptionList))
		}
	} else {
		// fetch the server list JSON and parse it into the `servers` array
		serverUrl := serverListUrl
		if str := c.String(defs.OptionServerJSON); str != "" {
			serverUrl = str
		}
		log.Infof("Retrieving server list from %s", serverUrl)

		servers, err = getServerList(c.Bool(defs.OptionSecure), serverUrl, c.IntSlice(defs.OptionExclude), c.IntSlice(defs.OptionServer), !c.Bool(defs.OptionList))

		if err != nil {
			log.Info("Retry with /.well-known/librespeed")
			servers, err = getServerList(c.Bool(defs.OptionSecure), serverUrl+"/.well-known/librespeed", c.IntSlice(defs.OptionExclude), c.IntSlice(defs.OptionServer), !c.Bool(defs.OptionList))
		}
	}
	if err != nil {
		log.Errorf("Error when fetching server list: %s", err)
		return err
	}

	// if --list is given, list all the servers fetched and exit
	if c.Bool(defs.OptionList) {
		for _, svr := range servers {
			var sponsorMsg string
			if svr.Sponsor() != "" {
				sponsorMsg = fmt.Sprintf(" [Sponsor: %s]", svr.Sponsor())
			}
			log.Warnf("%d: %s (%s) %s", svr.ID, svr.Name, svr.Server, sponsorMsg)
		}
		return nil
	}

	// if --server is given, do speed tests with all of them
	if len(c.IntSlice(defs.OptionServer)) > 0 {
		return doSpeedTest(c, servers, telemetryServer, network, silent)
	} else {
		// else select the fastest server from the list
		log.Info("Selecting the fastest server based on ping")

		var wg sync.WaitGroup
		jobs := make(chan PingJob, len(servers))
		results := make(chan PingResult, len(servers))
		done := make(chan struct{})

		pingList := make(map[int]float64)

		// spawn 10 concurrent pingers
		for i := 0; i < 10; i++ {
			go pingWorker(jobs, results, &wg, c.String(defs.OptionSource), network, c.Bool(defs.OptionNoICMP))
		}

		// send ping jobs to workers
		for idx, server := range servers {
			wg.Add(1)
			jobs <- PingJob{Index: idx, Server: server}
		}

		go func() {
			wg.Wait()
			close(done)
		}()

	Loop:
		for {
			select {
			case result := <-results:
				pingList[result.Index] = result.Ping
			case <-done:
				break Loop
			}
		}

		if len(pingList) == 0 {
			log.Fatal("No server is currently available, please try again later.")
		}

		// get the fastest server's index in the `servers` array
		var serverIdx int
		for idx, ping := range pingList {
			if ping > 0 && ping <= pingList[serverIdx] {
				serverIdx = idx
			}
		}

		// do speed test on the server
		return doSpeedTest(c, []defs.Server{servers[serverIdx]}, telemetryServer, network, silent)
	}
}

func pingWorker(jobs <-chan PingJob, results chan<- PingResult, wg *sync.WaitGroup, srcIp, network string, noICMP bool) {
	for {
		job := <-jobs
		server := job.Server
		// get the URL of the speed test server from the JSON
		u, err := server.GetURL()
		if err != nil {
			log.Debugf("Server URL is invalid for %s (%s), skipping", server.Name, server.Server)
			wg.Done()
			return
		}

		// check the server is up by accessing the ping URL and checking its returned value == empty and status code == 200
		if server.IsUp() {
			// skip ICMP if option given
			server.NoICMP = noICMP

			// if server is up, get ping
			ping, _, err := server.ICMPPingAndJitter(1, srcIp, network)
			if err != nil {
				log.Debugf("Can't ping server %s (%s), skipping", server.Name, u.Hostname())
				wg.Done()
				return
			}
			// return result
			results <- PingResult{Index: job.Index, Ping: ping}
			wg.Done()
		} else {
			log.Debugf("Server %s (%s) doesn't seem to be up, skipping", server.Name, u.Hostname())
			wg.Done()
		}
	}
}

// getServerList fetches the server JSON from a remote server
func getServerList(forceHTTPS bool, serverList string, excludes, specific []int, filter bool) ([]defs.Server, error) {
	// --exclude and --server cannot be used at the same time
	if len(excludes) > 0 && len(specific) > 0 {
		return nil, errors.New("either --exclude or --server can be used")
	}

	// getting the server list from remote
	var servers []defs.Server
	req, err := http.NewRequest(http.MethodGet, serverList, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", defs.UserAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}

	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := json.Unmarshal(b, &servers); err != nil {
		return nil, err
	}

	return preprocessServers(servers, forceHTTPS, excludes, specific, filter)
}

// getLocalServersReader loads the server JSON from an io.Reader
func getLocalServersReader(forceHTTPS bool, reader io.ReadCloser, excludes, specific []int, filter bool) ([]defs.Server, error) {
	defer reader.Close()

	var servers []defs.Server

	b, err := ioutil.ReadAll(reader)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(b, &servers); err != nil {
		return nil, err
	}

	return preprocessServers(servers, forceHTTPS, excludes, specific, filter)
}

// getLocalServers loads the server JSON from a local file
func getLocalServers(forceHTTPS bool, jsonFile string, excludes, specific []int, filter bool) ([]defs.Server, error) {
	f, err := os.OpenFile(jsonFile, os.O_RDONLY, 0644)
	if err != nil {
		return nil, err
	}
	return getLocalServersReader(forceHTTPS, f, excludes, specific, filter)
}

// preprocessServers makes some needed modifications to the servers fetched
func preprocessServers(servers []defs.Server, forceHTTPS bool, excludes, specific []int, filter bool) ([]defs.Server, error) {
	for i := range servers {
		u, err := servers[i].GetURL()
		if err != nil {
			return nil, err
		}

		// if no scheme is defined, use http as default, or https when --secure is given in cli options
		// if the scheme is predefined and --secure is not given, we will use it as-is
		if forceHTTPS {
			u.Scheme = "https"
		} else if u.Scheme == "" {
			// if `secure` is not used and no scheme is defined, use http
			u.Scheme = "http"
		}

		// modify the server struct in the array in place
		servers[i].Server = u.String()
	}

	if len(excludes) > 0 && len(specific) > 0 {
		return nil, errors.New("either --exclude or --specific can be used")
	}

	if filter {
		// exclude servers from --exclude
		if len(excludes) > 0 {
			var ret []defs.Server
			for _, server := range servers {
				if contains(excludes, server.ID) {
					continue
				}
				ret = append(ret, server)
			}
			return ret, nil
		}

		// use only servers from --server
		// special value -1 will test all servers
		if len(specific) > 0 && !contains(specific, -1) {
			var ret []defs.Server
			for _, server := range servers {
				if contains(specific, server.ID) {
					ret = append(ret, server)
				}
			}
			return ret, nil
		}
	}

	return servers, nil
}

// contains is a helper function to check if an int is in an int array
func contains(arr []int, val int) bool {
	for _, v := range arr {
		if v == val {
			return true
		}
	}
	return false
}
