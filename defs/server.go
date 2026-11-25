package defs

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"path"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
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

	// What the connection to this server actually negotiated; nil over
	// plain HTTP. Filled by CheckUp (and IsUp, which wraps it), which runs
	// before every measurement.
	NegotiatedTLS *TLSInfo `json:"-"`
	Protocol      string   `json:"-"` // e.g. HTTP/3.0, filled by IsUp too
}

// TLSInfo names the negotiated TLS parameters of a connection.
type TLSInfo struct {
	Version string
	Cipher  string
}

// maxProbeBody is how much of the ping response CheckUp reads.
const maxProbeBody = 512

// IsUp checks the speed test backend is up by accessing the ping URL
func (s *Server) IsUp() bool {
	return s.CheckUp() == nil
}

// CheckUp checks the speed test backend is up by accessing the ping URL,
// which must answer 200 with an empty body, and returns why it is not up.
// Like IsUp, it records the negotiated TLS in s.NegotiatedTLS.
func (s *Server) CheckUp() error {
	t := time.Now()
	defer func() {
		s.TLog.Logf("Check backend is up took %s", time.Since(t).String())
	}()

	u, err := s.GetURL()
	if err != nil {
		return err
	}
	u.Path = path.Join(u.Path, s.PingURL)

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		output.WriteDebug("Failed when creating HTTP request: %s\n", err)
		return err
	}
	req.Header.Set("User-Agent", UserAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		output.WriteDebug("Error checking for server status: %s\n", err)
		return err
	}
	defer resp.Body.Close()

	s.Protocol = resp.Proto

	// Report what the connection actually negotiated. On hardware without AES
	// acceleration the cipher, not the link, is what bounds the result, and
	// under TLS 1.3 the server picks it: the client offers a set and has no say
	// in the choice, and Go does not allow that set to be configured at all.
	// Two runs can therefore differ several-fold for a reason the numbers alone
	// do not show, which is what this line is for.
	if resp.TLS != nil {
		// Kept for the report as well as logged: the cipher explains the
		// number next to it, and a debug line is gone when the history
		// entry it would explain is read.
		s.NegotiatedTLS = &TLSInfo{
			Version: tls.VersionName(resp.TLS.Version),
			Cipher:  tls.CipherSuiteName(resp.TLS.CipherSuite),
		}
		output.WriteDebug("Negotiated %s with %s\n",
			s.NegotiatedTLS.Version, s.NegotiatedTLS.Cipher)
	} else {
		output.WriteDebug("Connection is not encrypted\n")
	}

	// Any body at all means the backend is down, so the start of it is
	// enough to decide and to show in the debug line.
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxProbeBody))
	if err != nil || len(b) > 0 {
		// %q rather than Sanitize: this is a raw response body where newlines are
		// legitimate, and quoting escapes control chars without losing them
		output.WriteDebug("Failed when parsing get IP result: %q\n", b)
	}
	// only return online if the ping URL returns nothing and 200; the status
	// first, since a 404 or 500 usually comes with an error page
	switch {
	case resp.StatusCode != http.StatusOK:
		// Go's phrase for the code; over HTTP/1.x resp.Status has the server's own
		if text := http.StatusText(resp.StatusCode); text != "" {
			return fmt.Errorf("the ping request returned HTTP %d %s", resp.StatusCode, text)
		}
		return fmt.Errorf("the ping request returned HTTP %d", resp.StatusCode)
	case err != nil:
		return fmt.Errorf("reading the ping response: %w", err)
	case len(b) > 0:
		return errors.New("the ping request returned a non-empty body")
	}
	return nil
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
		output.WriteDebug("Skipping ICMP for server %s, will use HTTP ping\n", output.Sanitize(s.Name))
		return s.PingAndJitter(count + 2)
	}

	u, err := s.GetURL()
	if err != nil {
		output.WriteDebug("Failed to get server URL: %s\n", err)
		return 0, 0, err
	}

	p, err := newPinger(u.Hostname(), network)
	if err != nil {
		output.WriteDebug("Failed to resolve ping target: %s\n", err)
		output.WriteDebug("Will try TCP ping\n")
		return s.PingAndJitter(count + 2)
	}
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

	// Say which address the test actually reached. IPv4 and IPv6 can take
	// different paths through the network, so a result is not fully described
	// by the hostname it was measured against, and --json carries no client
	// address to infer it from.
	if stats.IPAddr != nil {
		output.WriteDebug("Pinging %s over ICMP (%s)\n",
			stats.IPAddr.String(), addressFamily(stats.IPAddr.String()))
	}
	// A single figure hides how the samples were spread. The pinger already
	// works these out, and the spread is what says whether a link is steady or
	// merely fast on average. Raw counts rather than a loss percentage: ten
	// probes are too few for a rate, and ICMP is often policed independently of
	// the data path, so a percentage would say more about the server's ICMP
	// handling than about the network.
	output.WriteDebug("Ping over ICMP: min %.2f ms, avg %.2f ms, max %.2f ms, stddev %.2f ms, %d/%d replies\n",
		float64(stats.MinRtt.Microseconds())/1000,
		float64(stats.AvgRtt.Microseconds())/1000,
		float64(stats.MaxRtt.Microseconds())/1000,
		float64(stats.StdDevRtt.Microseconds())/1000,
		stats.PacketsRecv, stats.PacketsSent)

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
		output.WriteDebug("No ICMP pings returned for server %s (%s), trying TCP ping\n", output.Sanitize(s.Name), output.Sanitize(u.Hostname()))
		return s.PingAndJitter(count + 2)
	}

	return rttMillis(stats.AvgRtt), jitter, nil
}

// newPinger returns a pinger for host, resolved within network: "ip4" or
// "ip6" for that family, and anything else for either. The network is set
// before resolving: probing.NewPinger resolves at once with "ip", and a
// pinger that has an address does not resolve again.
func newPinger(host, network string) (*probing.Pinger, error) {
	p := probing.New(host)
	p.SetNetwork(network)
	if err := p.Resolve(); err != nil {
		return nil, err
	}
	return p, nil
}

// addressFamily names the IP version of an address, for reporting which path a
// measurement took. Accepts either a bare address or one with a port.
func addressFamily(addr string) string {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	switch {
	case ip == nil:
		return "unknown"
	case ip.To4() != nil:
		return "IPv4"
	default:
		return "IPv6"
	}
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

	// Collect every distinct peer, not just the first. The requests usually
	// share one connection, but a reconnect can land on a different address --
	// a different family, even -- and reporting only the first would then
	// describe a connection the later samples did not use.
	var remotes []string
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			if info.Conn == nil {
				return
			}
			addr := info.Conn.RemoteAddr().String()
			if !slices.Contains(remotes, addr) {
				remotes = append(remotes, addr)
			}
		},
	}))

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

	for _, addr := range remotes {
		output.WriteDebug("Pinging %s over %s (%s)\n", addr, s.Protocol, addressFamily(addr))
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
// streamProgress emits one progress event a second while a transfer phase
// runs, reading the rate off the phase's byte counter. The returned stop
// function ends the ticker and does not return until the goroutine is done,
// so a late progress event can never land after the next phase event.
func streamProgress(phase string, counter *BytesCounter, duration time.Duration) func() {
	if !output.StreamEnabled() {
		return func() {}
	}

	done := make(chan struct{})
	stopped := make(chan struct{})
	start := time.Now()

	go func() {
		defer close(stopped)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				elapsed := time.Since(start).Seconds()
				output.WriteEvent(output.ProgressEvent{
					Event:    "progress",
					Phase:    phase,
					Seconds:  math.Round(elapsed*10) / 10,
					Mbps:     math.Round(counter.AvgMbps()*100) / 100,
					Progress: int(math.Min(elapsed/duration.Seconds()*100, 100)),
				})
			}
		}
	}()

	return func() {
		close(done)
		<-stopped
	}
}

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

	// each finished request hands its worker back for a replacement
	downloadDone := make(chan int, requests)

	var wg sync.WaitGroup
	// whether any request was answered with data, and why the last one that
	// was not failed
	var succeeded atomic.Bool
	var lastErr atomic.Pointer[error]
	var failed bool

	// one read buffer per worker, reused by its replacement requests: only
	// one request per worker runs at a time
	bufs := make([][]byte, requests)
	for i := range bufs {
		bufs[i] = make([]byte, downloadReadSize)
	}

	// fetch makes one download request for a worker. An error means it
	// brought no test data: the request failed, or the answer was not a
	// success.
	fetch := func(worker int) error {
		resp, err := http.DefaultClient.Do(req.Clone(WithWorker(ctx, worker)))
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if err := refusal("download", resp); err != nil {
			// an error page is not test data, so it is not counted
			return err
		}

		// io.Copy into io.Discard would read in 8 KiB chunks (its ReadFrom),
		// and Go's HTTP/2 client sends a WINDOW_UPDATE frame, in its own
		// write, after every read that frees 4 KiB. Hiding ReadFrom makes
		// CopyBuffer read into the larger buffer instead.
		n, err := io.CopyBuffer(struct{ io.Writer }{io.Discard}, io.TeeReader(resp.Body, counter), bufs[worker])
		if n == 0 {
			// success without a single byte is not test data either
			if err == nil {
				err = errors.New("the download request returned no data")
			}
			return err
		}
		// A body that was cut short counts all the same, whoever cut it:
		// the end of the test, --timeout or the server. What arrived did
		// cross the link, and on a slow link no request gets its whole body.
		succeeded.Store(true)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			output.WriteDebug("Failed when reading HTTP response: %s\n", err)
		}
		return nil
	}

	doDownload := func(worker int) {
		defer wg.Done()

		if err := fetch(worker); err != nil {
			// cancellation is how the test ends, so it is not a failure
			if ctx.Err() != nil {
				return
			}
			output.WriteDebug("Download request failed: %s\n", err)
			lastErr.Store(&err)
			if !pauseBeforeRetry(ctx) {
				return
			}
		}

		// let the main loop start a replacement request, but never block on it
		// once the test is over, otherwise this goroutine is leaked
		select {
		case downloadDone <- worker:
		case <-ctx.Done():
		}
	}

	spawnDownload := func(worker int) {
		wg.Add(1)
		go doDownload(worker)
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
			if failed {
				return
			}
			if useBytes {
				output.WriteUI("Download rate:\t%s\n", counter.AvgHumanize())
			} else {
				output.WriteUI("Download rate:\t%.2f Mbps\n", counter.AvgMbps())
			}
		}()
	}

	for i := 0; i < requests; i++ {
		spawnDownload(i)
		time.Sleep(200 * time.Millisecond)
	}

	// One clock for the whole test window: the ticker and the timeout both
	// start here, after ramp-up, so reported progress cannot run ahead of the
	// test it describes.
	defer streamProgress("download", counter, duration)()
	timeout := time.After(duration)
Loop:
	for {
		select {
		case <-timeout:
			cancel()
			break Loop
		case worker := <-downloadDone:
			spawnDownload(worker)
		}
	}

	// let the cancelled requests unwind before reading the counter, so the
	// result doesn't change under us while it's being reported
	wg.Wait()

	// No request answered with data is not a result of 0 Mbps. Without an
	// error either, they were all still waiting when the test ended.
	if !succeeded.Load() {
		failed = true
		if err := lastErr.Load(); err != nil {
			return 0, 0, *err
		}
		return 0, 0, errors.New("no download request was answered before the test ended")
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

	if noPrealloc {
		output.WriteUI("Pre-allocation is disabled, performance might be lower!\n")
	} else {
		// Each request reads from this shared payload. Without it they
		// stream straight from crypto/rand instead, and the size is not used.
		counter.SetUploadSize(uploadSize)
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

	// each finished request hands its worker back for a replacement
	uploadDone := make(chan int, requests)

	var wg sync.WaitGroup
	// whether any request was accepted, whether one without an answer had
	// at least been sending for a while, and the last refusal and the last
	// other failure
	var succeeded, carried atomic.Bool
	var refusedWith, lastErr atomic.Pointer[error]
	var failed bool

	// The client follows a POST answered with 301, 302 or 303 as a GET
	// without the payload, and the answer to that GET would count as an
	// accepted upload. An upload is not sent on: a redirect is its answer.
	client := *http.DefaultClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	doUpload := func(worker int) {
		defer wg.Done()

		var bodyReader io.Reader
		sent := &sentBytes{total: counter}
		if noPrealloc {
			bodyReader = &SeekWrapper{rand.Reader}
		} else {
			bodyReader = bytes.NewReader(counter.Payload())
			sent.size = uint64(len(counter.Payload()))
		}
		countingReader := io.TeeReader(bodyReader, sent)

		uploadReq, err := http.NewRequestWithContext(WithWorker(ctx, worker), http.MethodPost, u.String(), countingReader)
		if err != nil {
			output.WriteDebug("Failed when creating HTTP request: %s\n", err)
			return
		}
		uploadReq.Header.Set("User-Agent", UserAgent)
		uploadReq.Header.Set("Accept-Encoding", "identity")

		if !noPrealloc {
			// Send Content-Length instead of Transfer-Encoding: chunked. The
			// TeeReader around the payload hides its length, so Go would
			// otherwise switch to chunked encoding. Hand-written HTTP servers
			// that do not decode chunked bodies (e.g. librespeed-rs) then block
			// waiting for an EOF that never comes while the client keeps the
			// connection open, hanging the upload after a single payload (see
			// librespeed/speedtest-cli#122). A fixed length is the standard,
			// universally supported form. Use the payload's actual size:
			// SetUploadSize scales the CLI's KiB option by 1024, so the request
			// length must match the generated blob, not the option value.
			uploadReq.ContentLength = int64(len(counter.Payload()))
		}

		resp, err := client.Do(uploadReq)
		// A request that was not accepted has still carried data when it
		// had been sending for a while, however it ended: with the test, at
		// the client timeout or on a dropped connection. A body that never
		// ends can also be cut by the server, with the status a server sends
		// once it has read as much as it takes. Any other error status is a
		// refusal, however long the request took to get it.
		timedOut := resp == nil && ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded)
		cut := noPrealloc && resp != nil && resp.StatusCode == http.StatusRequestEntityTooLarge && sent.started()
		streamed := cut || (resp == nil && sent.sending(timedOut))
		if streamed {
			carried.Store(true)
		}
		if err != nil && ctx.Err() != nil {
			// cancellation is how the test ends, so it is not a failure
			return
		}

		if resp != nil {
			// A request is accepted once the server answers it with success:
			// it has the payload then, whatever becomes of the rest of its
			// answer.
			if err = refusal("upload", resp); err == nil {
				succeeded.Store(true)
				if _, err := io.Copy(io.Discard, resp.Body); err != nil {
					// cancellation is how the test ends, so it is not a failure
					if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
						output.WriteDebug("Failed when reading HTTP response: %s\n", err)
					}
				}
			}
			// closed before the pause below, not after it
			resp.Body.Close()
		}

		// The client timeout cuts a request off while it is sending on a
		// slow uplink, and with --no-pre-allocate where the body never
		// ends. Such a request has not failed, and the next one follows it
		// at once. Any other error is a failure.
		if err != nil && !(streamed && timedOut) {
			output.WriteDebug("Upload request failed: %s\n", err)
			switch {
			case streamed:
				// what was sent before the request was cut stays counted
				lastErr.Store(&err)
			case resp != nil:
				// what a refused request sent stays counted as well: it
				// did cross the link
				refusedWith.Store(&err)
			case timedOut:
				// The client timeout ended a request that had handed its
				// payload over at once, or that sent nothing. What it sent
				// may still be on its way, so it stays counted.
				lastErr.Store(&err)
			default:
				// A connection that is dropped at once took in no more
				// than the buffers on the way hold. Dropped requests are
				// replaced several times a second, and that would add up.
				lastErr.Store(&err)
				sent.discount()
			}
			// The body cannot be sent again, so the transport does not
			// retry a failed request. The main loop starts a new one
			// instead, after a pause. In auto mode a failed HTTP/3 request
			// has already moved the server to TCP, so the new request goes
			// over TCP.
			if !pauseBeforeRetry(ctx) {
				return
			}
		}

		// let the main loop start a replacement request, but never block on it
		// once the test is over, otherwise this goroutine is leaked
		select {
		case uploadDone <- worker:
		case <-ctx.Done():
		}
	}

	spawnUpload := func(worker int) {
		wg.Add(1)
		go doUpload(worker)
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
			if failed {
				return
			}
			if useBytes {
				output.WriteUI("Upload rate:\t%s\n", counter.AvgHumanize())
			} else {
				output.WriteUI("Upload rate:\t%.2f Mbps\n", counter.AvgMbps())
			}
		}()
	}

	for i := 0; i < requests; i++ {
		spawnUpload(i)
		time.Sleep(200 * time.Millisecond)
	}

	// One clock for the whole test window: the ticker and the timeout both
	// start here, after ramp-up, so reported progress cannot run ahead of the
	// test it describes.
	defer streamProgress("upload", counter, duration)()
	timeout := time.After(duration)
Loop:
	for {
		select {
		case <-timeout:
			cancel()
			break Loop
		case worker := <-uploadDone:
			spawnUpload(worker)
		}
	}

	// let the cancelled requests unwind before reading the counter, so the
	// result doesn't change under us while it's being reported
	wg.Wait()

	// No accepted request is a failure when the server refused one: that is
	// the only answer it gave. Without any answer the upload can still be a
	// measurement. On a slow uplink no payload gets through before the
	// client timeout or the end of the test cuts its request off, and with
	// --no-pre-allocate the body never ends. What was sent is reported
	// then, as it always was, as long as some request had been sending for
	// a while.
	//
	// A server that takes the payload and never answers can look like a
	// slow uplink from here, where the buffers on the way hold the payload.
	// It is told apart only where the payload was handed over at once.
	if !succeeded.Load() {
		if err := refusedWith.Load(); err != nil {
			failed = true
			return 0, 0, *err
		}
		if !carried.Load() {
			failed = true
			if err := lastErr.Load(); err != nil {
				return 0, 0, *err
			}
			return 0, 0, errors.New("no upload request was answered before the test ended")
		}
	}
	return counter.AvgMbps(), counter.Total(), nil
}

// sendingProof is how long ago a request has to have sent its first byte
// before what it sent stands as a measurement without an answer. A
// connection that is dropped at once does not get that far.
const sendingProof = 250 * time.Millisecond

// sentBytes counts what one upload request sends, next to the counter of
// the whole test, and keeps the time of its first byte and of the last byte
// of its payload. The client may still read the body after it has returned,
// so everything is locked.
type sentBytes struct {
	total *BytesCounter
	// the length of the payload, zero for a body that never ends
	size uint64

	mu    sync.Mutex
	n     uint64
	first time.Time
	whole time.Time
	gone  bool
}

func (s *sentBytes) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gone {
		return len(p), nil
	}
	if s.first.IsZero() {
		s.first = time.Now()
	}
	s.n += uint64(len(p))
	if s.size > 0 && s.n >= s.size && s.whole.IsZero() {
		s.whole = time.Now()
	}
	return s.total.Write(p)
}

// started reports whether the request has sent anything.
func (s *sentBytes) started() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.first.IsZero()
}

// sending reports whether the request has been sending: for sendingProof
// since its first byte, or for any time at all when it is known to have
// lasted, as one does that the client timeout cut off. A payload that was
// handed over whole within sendingProof is not being sent any more. The
// buffers on the way took it in, or the server did, and the request has
// only waited for its answer since.
func (s *sentBytes) sending(lasted bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.first.IsZero() || (!lasted && time.Since(s.first) < sendingProof) {
		return false
	}
	return s.whole.IsZero() || s.whole.Sub(s.first) >= sendingProof
}

// discount takes what the request has sent back out of the count of the
// test, and keeps out what it may still send.
func (s *sentBytes) discount() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gone = true
	s.total.Discount(s.n)
	s.n = 0
}

// downloadReadSize is how much each download read asks for. On loopback,
// where the CPU is the limit, HTTP/2 downloads got faster up to 128-256 KiB
// and no faster beyond. It stays well below the 4 MiB stream window of Go's
// HTTP/2 client, so the server is never left waiting for window.
const downloadReadSize = 256 << 10

// retryDelay spaces the requests that follow a failed one, so that a wrong
// URL is not asked for as fast as the server can refuse it.
const retryDelay = 200 * time.Millisecond

// errorPageLimit is how much of a refused answer is read before it is closed.
const errorPageLimit = 4 << 10

// refusal reports a transfer request that was not answered with success.
// Any 2xx status is success. Everything else comes with an error page or
// nothing, not with test data, and that includes a redirect the client did
// not follow.
//
// A short error page is read to its end, so that the retry can use the
// connection again and does not open a new one each time. Only newer Go
// versions do that on their own. A page that does not say how long it is
// is left alone: reading it could wait for a server that never ends it.
func refusal(phase string, resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return nil
	}
	if n := resp.ContentLength; n > 0 && n <= errorPageLimit {
		io.CopyN(io.Discard, resp.Body, n)
	}
	if text := http.StatusText(resp.StatusCode); text != "" {
		return fmt.Errorf("the %s request returned HTTP %d %s", phase, resp.StatusCode, text)
	}
	return fmt.Errorf("the %s request returned HTTP %d", phase, resp.StatusCode)
}

// pauseBeforeRetry waits retryDelay and reports whether the test is still on.
func pauseBeforeRetry(ctx context.Context) bool {
	select {
	case <-time.After(retryDelay):
		return true
	case <-ctx.Done():
		return false
	}
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
			// %q rather than Sanitize: see CheckUp
			output.WriteDebug("Received payload: %q\n", b)
			// A server may return rawIspInfo as an empty string rather than an
			// object (or wrap the whole payload in processedString); recover
			// whatever fields parse instead of losing everything.
			var partial struct {
				ProcessedString string          `json:"processedString"`
				RawISPInfo      json.RawMessage `json:"rawIspInfo"`
			}
			if err2 := json.Unmarshal(b, &partial); err2 == nil {
				if partial.ProcessedString != "" {
					ipInfo.ProcessedString = partial.ProcessedString
				} else {
					ipInfo.ProcessedString = string(b)
				}
				ipInfo.RawISPInfo = partial.RawISPInfo
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
