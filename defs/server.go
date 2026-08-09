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
	"path"
	"strconv"
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
		output.WriteDebug("Failed when parsing get IP result: %s\n", b)
		return false
	}
	// only return online if the ping URL returns nothing and 200
	return resp.StatusCode == http.StatusOK
}

// rttMillis converts a round-trip time to milliseconds without discarding the
// fraction. Duration.Milliseconds truncates to whole milliseconds, which on a
// link faster than that leaves nothing to report.
func rttMillis(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

// ICMPPingAndJitter pings the server via ICMP echos and calculate the average ping and jitter
func (s *Server) ICMPPingAndJitter(count int, srcIp, network string) (float64, float64, error) {
	t := time.Now()
	defer func() {
		s.TLog.Logf("ICMP ping took %s", time.Since(t).String())
	}()

	if s.NoICMP {
		output.WriteDebug("Skipping ICMP for server %s, will use HTTP ping\n", s.Name)
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
			instJitter := math.Abs(lastPing - rttMillis(rtt))
			if idx > 1 {
				if jitter > instJitter {
					jitter = jitter*0.7 + instJitter*0.3
				} else {
					jitter = instJitter*0.2 + jitter*0.8
				}
			}
		}
		lastPing = rttMillis(rtt)
	}

	if len(stats.Rtts) == 0 {
		s.NoICMP = true
		output.WriteDebug("No ICMP pings returned for server %s (%s), trying TCP ping\n", s.Name, u.Hostname())
		return s.PingAndJitter(count + 2)
	}

	return rttMillis(stats.AvgRtt), jitter, nil
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

		pings = append(pings, rttMillis(end.Sub(start)))
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

	doDownload := func() {
		reqClone := req.Clone(ctx)
		resp, err := http.DefaultClient.Do(reqClone)
		if err != nil {
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				output.WriteDebug("Failed when making HTTP request: %s\n", err)
			}
		} else {
			defer resp.Body.Close()

			if _, err = io.Copy(io.Discard, io.TeeReader(resp.Body, counter)); err != nil {
				if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
					output.WriteDebug("Failed when reading HTTP response: %s\n", err)
				}
			}

			downloadDone <- struct{}{}
		}
	}

	counter.Start()
	if !silent {
		pb := spinner.New(spinner.CharSets[11], 100*time.Millisecond)
		pb.Prefix = "Downloading...  "
		pb.PostUpdate = func(s *spinner.Spinner) {
			if useBytes {
				s.Suffix = fmt.Sprintf("  %s", counter.AvgHumanize())
			} else {
				s.Suffix = fmt.Sprintf("  %.2f Mbps", counter.AvgMbps())
			}
		}

		pb.Start()
		defer func() {
			if useBytes {
				pb.FinalMSG = fmt.Sprintf("Download rate:\t%s\n", counter.AvgHumanize())
			} else {
				pb.FinalMSG = fmt.Sprintf("Download rate:\t%.2f Mbps\n", counter.AvgMbps())
			}
			pb.Stop()
		}()
	}

	for i := 0; i < requests; i++ {
		go doDownload()
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
			go doDownload()
		}
	}

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
		counter.reader = &SeekWrapper{rand.Reader}
	} else {
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

	doUpload := func() {
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
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			output.WriteDebug("Failed when making HTTP request: %s\n", err)
		} else if err == nil {
			defer resp.Body.Close()
			if _, err := io.Copy(io.Discard, resp.Body); err != nil {
				output.WriteDebug("Failed when reading HTTP response: %s\n", err)
			}

			uploadDone <- struct{}{}
		}
	}

	counter.Start()
	if !silent {
		pb := spinner.New(spinner.CharSets[11], 100*time.Millisecond)
		pb.Prefix = "Uploading...  "
		pb.PostUpdate = func(s *spinner.Spinner) {
			if useBytes {
				s.Suffix = fmt.Sprintf("  %s", counter.AvgHumanize())
			} else {
				s.Suffix = fmt.Sprintf("  %.2f Mbps", counter.AvgMbps())
			}
		}

		pb.Start()
		defer func() {
			if useBytes {
				pb.FinalMSG = fmt.Sprintf("Upload rate:\t%s\n", counter.AvgHumanize())
			} else {
				pb.FinalMSG = fmt.Sprintf("Upload rate:\t%.2f Mbps\n", counter.AvgMbps())
			}
			pb.Stop()
		}()
	}

	for i := 0; i < requests; i++ {
		go doUpload()
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
			go doUpload()
		}
	}

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
			output.WriteDebug("Received payload: %s\n", b)
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
				output.WriteDebug("Sponsor URL is invalid: %s\n", s.SponsorURL)
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
