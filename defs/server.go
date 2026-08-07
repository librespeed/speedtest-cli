package defs

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"sync"
	"time"

	"github.com/briandowns/spinner"
	"github.com/librespeed/speedtest-cli/output"
	probing "github.com/prometheus-community/pro-bing"
)

// Server represents a speed test server
type Server struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Server      string `json:"server"`
	DownloadURL string `json:"dlURL"`
	UploadURL   string `json:"ulURL"`
	PingURL     string `json:"pingURL"`
	GetIPURL    string `json:"getIpURL"`
	SponsorName string `json:"sponsorName"`
	SponsorURL  string `json:"sponsorURL"`

	NoICMP bool         `json:"-"`
	TLog   TelemetryLog `json:"-"`
}

// IsUp checks the speed test backend is up by accessing the ping URL
func (s *Server) IsUp() bool {
	t := time.Now()
	defer func() {
		s.TLog.Logf("Check backend is up took %s", time.Since(t).String())
	}()

	u, _ := s.GetURL()
	u.Path = path.Join(u.Path, s.PingURL)

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		output.WriteDebug("Failed when creating HTTP request: %s\n", err)
		return false
	}
	req.Header.Set("User-Agent", UserAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		output.WriteDebug("Error checking for server status: %s\n", err)
		return false
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil || len(b) > 0 {
		// %q rather than Sanitize: this is a raw response body where newlines are
		// legitimate, and quoting escapes control chars without losing them
		output.WriteDebug("Failed when parsing get IP result: %q\n", b)
		return false
	}
	// only return online if the ping URL returns nothing and 200
	return resp.StatusCode == http.StatusOK
}

// ICMPPingAndJitter pings the server via ICMP echos and calculate the average ping and jitter
func (s *Server) ICMPPingAndJitter(count int, srcIp, network string) (float64, float64, error) {
	t := time.Now()
	defer func() {
		s.TLog.Logf("ICMP ping took %s", time.Since(t).String())
	}()

	if s.NoICMP {
		output.WriteDebug("Skipping ICMP for server %s, will use HTTP ping\n", output.Sanitize(s.Name))
		return s.PingAndJitter(count + 2)
	}

	u, err := s.GetURL()
	if err != nil {
		output.WriteDebug("Failed to get server URL: %s\n", err)
		return 0, 0, err
	}

	p, err := probing.NewPinger(u.Hostname())
	if err != nil {
		output.WriteDebug("Failed to resolve ping target: %s\n", err)
		output.WriteDebug("Will try TCP ping\n")
		return s.PingAndJitter(count + 2)
	}
	p.SetNetwork(network)
	p.Count = count
	p.Timeout = time.Duration(count) * time.Second
	if srcIp != "" {
		p.Source = srcIp
	}
	if output.IsDebug() {
		p.Debug = true
	}
	if err := p.Run(); err != nil {
		output.WriteDebug("Failed to ping target host: %s\n", err)
		output.WriteDebug("Will try TCP ping\n")
		return s.PingAndJitter(count + 2)
	}

	stats := p.Statistics()

	var lastPing, jitter float64
	for idx, rtt := range stats.Rtts {
		if idx != 0 {
			instJitter := math.Abs(lastPing - float64(rtt.Milliseconds()))
			if idx > 1 {
				if jitter > instJitter {
					jitter = jitter*0.7 + instJitter*0.3
				} else {
					jitter = instJitter*0.2 + jitter*0.8
				}
			}
		}
		lastPing = float64(rtt.Milliseconds())
	}

	if len(stats.Rtts) == 0 {
		s.NoICMP = true
		output.WriteDebug("No ICMP pings returned for server %s (%s), trying TCP ping\n", output.Sanitize(s.Name), output.Sanitize(u.Hostname()))
		return s.PingAndJitter(count + 2)
	}

	return float64(stats.AvgRtt.Milliseconds()), jitter, nil
}

// PingAndJitter pings the server via accessing ping URL and calculate the average ping and jitter
func (s *Server) PingAndJitter(count int) (float64, float64, error) {
	t := time.Now()
	defer func() {
		s.TLog.Logf("TCP ping took %s", time.Since(t).String())
	}()

	u, err := s.GetURL()
	if err != nil {
		output.WriteDebug("Failed to get server URL: %s\n", err)
		return 0, 0, err
	}
	u.Path = path.Join(u.Path, s.PingURL)

	var pings []float64

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		output.WriteDebug("Failed when creating HTTP request: %s\n", err)
		return 0, 0, err
	}
	req.Header.Set("User-Agent", UserAgent)

	for i := 0; i < count; i++ {
		start := time.Now()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			output.WriteDebug("Failed when making HTTP request: %s\n", err)
			return 0, 0, err
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		end := time.Now()

		pings = append(pings, float64(end.Sub(start).Milliseconds()))
	}

	// discard first result due to handshake overhead
	if len(pings) > 1 {
		pings = pings[1:]
	}

	var lastPing, jitter float64
	for idx, p := range pings {
		if idx != 0 {
			instJitter := math.Abs(lastPing - p)
			if idx > 1 {
				if jitter > instJitter {
					jitter = jitter*0.7 + instJitter*0.3
				} else {
					jitter = instJitter*0.2 + jitter*0.8
				}
			}
		}
		lastPing = p
	}

	return getAvg(pings), jitter, nil
}

// Download performs the actual download test
func (s *Server) Download(silent bool, useBytes, useMebi bool, requests int, chunks int, duration time.Duration) (float64, uint64, error) {
	t := time.Now()
	defer func() {
		s.TLog.Logf("Download took %s", time.Since(t).String())
	}()

	counter := NewCounter()
	counter.SetMebi(useMebi)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	u, err := s.GetURL()
	if err != nil {
		output.WriteDebug("Failed to get server URL: %s\n", err)
		return 0, 0, err
	}

	u.Path = path.Join(u.Path, s.DownloadURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		output.WriteDebug("Failed when creating HTTP request: %s\n", err)
		return 0, 0, err
	}
	q := req.URL.Query()
	q.Set("ckSize", strconv.Itoa(chunks))
	req.URL.RawQuery = q.Encode()
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept-Encoding", "identity")

	downloadDone := make(chan struct{}, requests)

	var wg sync.WaitGroup

	doDownload := func() {
		defer wg.Done()

		reqClone := req.Clone(ctx)
		resp, err := http.DefaultClient.Do(reqClone)
		if err != nil {
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				output.WriteDebug("Failed when making HTTP request: %s\n", err)
			}
			return
		}
		defer resp.Body.Close()

		if _, err = io.Copy(io.Discard, io.TeeReader(resp.Body, counter)); err != nil {
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				output.WriteDebug("Failed when reading HTTP response: %s\n", err)
			}
		}

		// let the main loop start a replacement request, but never block on it
		// once the test is over, otherwise this goroutine is leaked
		select {
		case downloadDone <- struct{}{}:
		case <-ctx.Done():
		}
	}

	spawnDownload := func() {
		wg.Add(1)
		go doDownload()
	}

	counter.Start()
	if !silent {
		pb := spinner.New(spinner.CharSets[11], 100*time.Millisecond, spinner.WithWriterFile(os.Stderr))
		pb.Prefix = "Downloading...  "
		pb.PostUpdate = func(s *spinner.Spinner) {
			if useBytes {
				s.Suffix = fmt.Sprintf("  %s", counter.AvgHumanize())
			} else {
				s.Suffix = fmt.Sprintf("  %.2f Mbps", counter.AvgMbps())
			}
		}

		pb.Start()
		// print the rate ourselves instead of via pb.FinalMSG: the spinner only
		// prints it when it was actually running, which it isn't when stderr is
		// not a terminal
		defer func() {
			pb.Stop()
			if useBytes {
				output.WriteUI("Download rate:\t%s\n", counter.AvgHumanize())
			} else {
				output.WriteUI("Download rate:\t%.2f Mbps\n", counter.AvgMbps())
			}
		}()
	}

	for i := 0; i < requests; i++ {
		spawnDownload()
		time.Sleep(200 * time.Millisecond)
	}
	timeout := time.After(duration)
Loop:
	for {
		select {
		case <-timeout:
			cancel()
			break Loop
		case <-downloadDone:
			spawnDownload()
		}
	}

	// let the cancelled requests unwind before reading the counter, so the
	// result doesn't change under us while it's being reported
	wg.Wait()

	return counter.AvgMbps(), counter.Total(), nil
}

// Upload performs the actual upload test
func (s *Server) Upload(noPrealloc, silent, useBytes, useMebi bool, requests int, uploadSize int, duration time.Duration) (float64, uint64, error) {
	t := time.Now()
	defer func() {
		s.TLog.Logf("Upload took %s", time.Since(t).String())
	}()

	counter := NewCounter()
	counter.SetMebi(useMebi)
	counter.SetUploadSize(uploadSize)

	if noPrealloc {
		output.WriteUI("Pre-allocation is disabled, performance might be lower!\n")
	} else {
		// each request reads from this shared payload; without it they stream
		// straight from crypto/rand instead
		counter.GenerateBlob()
	}

	u, err := s.GetURL()
	if err != nil {
		output.WriteDebug("Failed to get server URL: %s\n", err)
		return 0, 0, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	u.Path = path.Join(u.Path, s.UploadURL)

	uploadDone := make(chan struct{}, requests)

	var wg sync.WaitGroup

	doUpload := func() {
		defer wg.Done()

		var bodyReader io.Reader
		if noPrealloc {
			bodyReader = &SeekWrapper{rand.Reader}
		} else {
			bodyReader = bytes.NewReader(counter.Payload())
		}
		countingReader := io.TeeReader(bodyReader, counter)

		uploadReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), countingReader)
		if err != nil {
			output.WriteDebug("Failed when creating HTTP request: %s\n", err)
			return
		}
		uploadReq.Header.Set("User-Agent", UserAgent)
		uploadReq.Header.Set("Accept-Encoding", "identity")

		resp, err := http.DefaultClient.Do(uploadReq)
		if err != nil {
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				output.WriteDebug("Failed when making HTTP request: %s\n", err)
			}
			return
		}
		defer resp.Body.Close()

		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			// cancellation is how the test ends, so it is not a failure
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				output.WriteDebug("Failed when reading HTTP response: %s\n", err)
			}
		}

		// let the main loop start a replacement request, but never block on it
		// once the test is over, otherwise this goroutine is leaked
		select {
		case uploadDone <- struct{}{}:
		case <-ctx.Done():
		}
	}

	spawnUpload := func() {
		wg.Add(1)
		go doUpload()
	}

	counter.Start()
	if !silent {
		pb := spinner.New(spinner.CharSets[11], 100*time.Millisecond, spinner.WithWriterFile(os.Stderr))
		pb.Prefix = "Uploading...  "
		pb.PostUpdate = func(s *spinner.Spinner) {
			if useBytes {
				s.Suffix = fmt.Sprintf("  %s", counter.AvgHumanize())
			} else {
				s.Suffix = fmt.Sprintf("  %.2f Mbps", counter.AvgMbps())
			}
		}

		pb.Start()
		// print the rate ourselves instead of via pb.FinalMSG: the spinner only
		// prints it when it was actually running, which it isn't when stderr is
		// not a terminal
		defer func() {
			pb.Stop()
			if useBytes {
				output.WriteUI("Upload rate:\t%s\n", counter.AvgHumanize())
			} else {
				output.WriteUI("Upload rate:\t%.2f Mbps\n", counter.AvgMbps())
			}
		}()
	}

	for i := 0; i < requests; i++ {
		spawnUpload()
		time.Sleep(200 * time.Millisecond)
	}
	timeout := time.After(duration)
Loop:
	for {
		select {
		case <-timeout:
			cancel()
			break Loop
		case <-uploadDone:
			spawnUpload()
		}
	}

	// let the cancelled requests unwind before reading the counter, so the
	// result doesn't change under us while it's being reported
	wg.Wait()

	return counter.AvgMbps(), counter.Total(), nil
}

// GetIPInfo accesses the backend's getIP.php endpoint and get current client's IP information
func (s *Server) GetIPInfo(distanceUnit string) (*GetIPResult, error) {
	t := time.Now()
	defer func() {
		s.TLog.Logf("Get IP info took %s", time.Since(t).String())
	}()

	var ipInfo GetIPResult
	u, err := s.GetURL()
	if err != nil {
		output.WriteDebug("Failed to get server URL: %s\n", err)
		return nil, err
	}
	u.Path = path.Join(u.Path, s.GetIPURL)
	q := u.Query()
	q.Set("isp", "true")
	q.Set("distance", distanceUnit)
	u.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		output.WriteDebug("Failed when creating HTTP request: %s\n", err)
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		output.WriteDebug("Failed when making HTTP request: %s\n", err)
		return nil, err
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		output.WriteDebug("Failed when reading HTTP response: %s\n", err)
		return nil, err
	}

	if len(b) > 0 {
		if err := json.Unmarshal(b, &ipInfo); err != nil {
			output.WriteDebug("Failed when parsing get IP result: %s\n", err)
			// %q rather than Sanitize: see IsUp
			output.WriteDebug("Received payload: %q\n", b)
			// try to extract processedString even if full parse fails
			// (e.g. when rawIspInfo is "" instead of an object)
			var partial struct {
				ProcessedString string `json:"processedString"`
			}
			if err2 := json.Unmarshal(b, &partial); err2 == nil && partial.ProcessedString != "" {
				ipInfo.ProcessedString = partial.ProcessedString
			} else {
				ipInfo.ProcessedString = string(b)
			}
		}
	}

	return &ipInfo, nil
}

// GetURL parses the server's URL into a url.URL
func (s *Server) GetURL() (*url.URL, error) {
	t := time.Now()
	defer func() {
		s.TLog.Logf("Parse server URL took %s", time.Since(t).String())
	}()

	u, err := url.Parse(s.Server)
	if err != nil {
		output.WriteDebug("Failed when parsing server URL: %s\n", err)
		return u, err
	}
	return u, nil
}

// Sponsor returns the sponsor's info
func (s *Server) Sponsor() string {
	var sponsorMsg string
	if s.SponsorName != "" {
		sponsorMsg += s.SponsorName

		if s.SponsorURL != "" {
			su, err := url.Parse(s.SponsorURL)
			if err != nil {
				output.WriteDebug("Sponsor URL is invalid: %s\n", output.Sanitize(s.SponsorURL))
			} else {
				if su.Scheme == "" {
					su.Scheme = "https"
				}
				sponsorMsg += " @ " + su.String()
			}
		}
	}
	return sponsorMsg
}
