package speedtest

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	"github.com/librespeed/speedtest-cli/defs"
	"github.com/librespeed/speedtest-cli/output"
)

// maxH3Handshake caps how long auto mode waits for a QUIC handshake before
// it falls back to TCP. Networks that drop UDP give no error, only silence.
const maxH3Handshake = 3 * time.Second

// protoTransport sends requests to the test servers over the HTTP version
// given with --http-version; other requests (server list, telemetry) use base.
// In auto mode a server moves to HTTP/3 once it advertises it via Alt-Svc,
// and back to TCP if QUIC then fails. A forced version never falls back.
// HTTP/3 is never used through a proxy: QUIC cannot go through one.
// Each download and upload worker gets its own connection: HTTP/2 and HTTP/3
// would otherwise carry every stream over one connection, with one
// congestion window, where HTTP/1.1 opens one per stream.
type protoTransport struct {
	version string
	base    *http.Transport
	tcp     *http.Transport // requests outside the workers: probe, IP info
	h3      *http3.Transport
	newH3   func() *http3.Transport

	mu       sync.Mutex
	hosts    map[string]string // test servers: "", "h3" or "tcp" in auto mode
	observed map[string]*observation
	workers  map[int]workerTransports
}

// workerTransports are the transports of one download or upload worker.
type workerTransports struct {
	tcp *http.Transport
	h3  *http3.Transport
}

// observation is what the responses from one test server actually ran over.
type observation struct {
	protocols []string // in order of first use
	tls       *tls.ConnectionState
}

// newProtoTransport wraps tcp. timeout is the client's timeout, which bounds
// the QUIC handshake in auto mode so a TCP retry still fits in it.
func newProtoTransport(version string, tcp *http.Transport, network string, dialer *net.Dialer, timeout time.Duration) (*protoTransport, error) {
	t := &protoTransport{version: version, tcp: tcp, hosts: map[string]string{}, observed: map[string]*observation{}, workers: map[int]workerTransports{}}
	switch version {
	case "auto", "3":
	case "1", "1.1":
		t.tcp.Protocols = new(http.Protocols)
		t.tcp.Protocols.SetHTTP1(true)
	case "2":
		t.tcp.Protocols = new(http.Protocols)
		t.tcp.Protocols.SetHTTP2(true)
		t.tcp.Protocols.SetUnencryptedHTTP2(true)
	default:
		return nil, fmt.Errorf("unsupported HTTP version %q, use one of auto, 1.1, 2, 3", version)
	}
	// clone only once Protocols is set: Clone fixes the source's protocols
	t.base = tcp.Clone()
	t.base.Protocols = nil
	quicConf := &quic.Config{
		// quic-go's default of 1280 does not fit the 1280-byte MTU of
		// WireGuard/Tailscale tunnels (quic-go/quic-go#5573)
		InitialPacketSize: 1200,
	}
	if version == "auto" {
		quicConf.HandshakeIdleTimeout = handshakeBudget(timeout)
	}
	t.newH3 = func() *http3.Transport {
		return &http3.Transport{
			TLSClientConfig: tcp.TLSClientConfig.Clone(),
			QUICConfig:      quicConf.Clone(),
			Dial: func(ctx context.Context, addr string, tlsConf *tls.Config, quicConf *quic.Config) (*quic.Conn, error) {
				return dialQUIC(ctx, network, dialer, addr, tlsConf, quicConf)
			},
		}
	}
	t.h3 = t.newH3()
	return t, nil
}

// transportsFor returns the transports of the worker that sent req, made on
// its first request, or the shared ones for a request outside the workers.
func (t *protoTransport) transportsFor(req *http.Request) (*http.Transport, *http3.Transport) {
	worker, ok := defs.Worker(req.Context())
	if !ok {
		return t.tcp, t.h3
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	w, ok := t.workers[worker]
	if !ok {
		w = workerTransports{tcp: t.tcp.Clone(), h3: t.newH3()}
		t.workers[worker] = w
	}
	return w.tcp, w.h3
}

// handshakeBudget leaves at least two thirds of the client timeout for TCP.
func handshakeBudget(timeout time.Duration) time.Duration {
	if timeout > 0 && timeout/3 < maxH3Handshake {
		return timeout / 3
	}
	return maxH3Handshake
}

// addServer marks a host as a test server, to be reached over --http-version.
func (t *protoTransport) addServer(host string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.hosts[host]; !ok {
		t.hosts[host] = ""
	}
}

func (t *protoTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	state, isServer := t.hosts[req.URL.Host]
	t.mu.Unlock()
	if !isServer {
		return sanitizingTransport{t.base}.RoundTrip(req)
	}
	resp, err := sanitizingTransport{roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return t.roundTrip(req, state)
	})}.RoundTrip(req)
	if err == nil {
		t.observe(req.URL.Host, resp)
	}
	return resp, err
}

// roundTripFunc lets a function serve as an http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// sanitizingTransport cleans the errors of rt and of the bodies it returns.
type sanitizingTransport struct{ rt http.RoundTripper }

func (s sanitizingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := s.rt.RoundTrip(req)
	if err != nil {
		return nil, sanitizedError{err}
	}
	resp.Body = sanitizedBody{resp.Body}
	return resp, nil
}

// sanitizedError hides control characters in a network error, which can
// carry text from the server (an HTTP/3 close reason, a certificate name).
// Callers print it, so it is cleaned where every request passes. Unwrap
// keeps errors.Is working, for context.Canceled in particular.
type sanitizedError struct{ err error }

func (e sanitizedError) Error() string {
	// errors.Join puts each address's error on its own line
	return output.Sanitize(strings.ReplaceAll(e.err.Error(), "\n", "; "))
}

func (e sanitizedError) Unwrap() error { return e.err }

// sanitizedBody does the same for errors met while reading the body.
type sanitizedBody struct{ io.ReadCloser }

func (b sanitizedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && err != io.EOF {
		err = sanitizedError{err}
	}
	return n, err
}

func (t *protoTransport) roundTrip(req *http.Request, state string) (*http.Response, error) {
	tcp, h3 := t.transportsFor(req)
	switch {
	case t.version == "3":
		if req.URL.Scheme != "https" {
			err := errors.New("HTTP/3 requires https, try --secure")
			output.WriteError("%s\n", h3Failure(req.URL.Host, err))
			return nil, err
		}
		if proxy, err := t.proxyFor(req); proxy != "" || err != nil {
			err = proxyError(proxy, err)
			output.WriteError("%s\n", h3Failure(req.URL.Host, err))
			return nil, err
		}
		resp, err := h3.RoundTrip(req)
		if err != nil && req.Context().Err() == nil {
			output.WriteError("%s\n", h3Failure(req.URL.Host, err))
		}
		return resp, err
	case t.version != "auto" || req.URL.Scheme != "https":
		return tcp.RoundTrip(req)
	}

	// auto: a proxy keeps every request on TCP, through the proxy
	if proxy, err := t.proxyFor(req); proxy != "" || err != nil {
		return tcp.RoundTrip(req)
	}

	if state == "h3" {
		resp, err := h3.RoundTrip(req)
		// no time left for TCP once the caller's context is done
		if err == nil || req.Context().Err() != nil {
			return resp, err
		}
		output.WriteUI("%s, falling back to TCP\n", h3Failure(req.URL.Host, err))
		t.setState(req.URL.Host, "tcp")
		if !replayable(req) {
			return nil, err
		}
		return tcp.RoundTrip(req)
	}

	resp, err := tcp.RoundTrip(req)
	if err != nil || state != "" || !advertisesH3(resp, req.URL) {
		return resp, err
	}
	output.WriteDebug("%s advertises HTTP/3, switching to it\n", output.Sanitize(req.URL.Host))
	t.setState(req.URL.Host, "h3")
	// repeat the request over HTTP/3 when possible, so what CheckUp reports
	// is what the test then runs over
	if !replayable(req) {
		return resp, nil
	}
	resp.Body.Close()
	return t.roundTrip(req, "h3")
}

// proxyFor returns the proxy the TCP transport would send req through.
func (t *protoTransport) proxyFor(req *http.Request) (string, error) {
	if t.tcp.Proxy == nil {
		return "", nil
	}
	u, err := t.tcp.Proxy(req)
	if err != nil || u == nil {
		return "", err
	}
	return u.Redacted(), nil
}

func proxyError(proxy string, err error) error {
	if err != nil {
		return fmt.Errorf("cannot check the proxy settings: %w", err)
	}
	return fmt.Errorf("HTTP/3 cannot go through the proxy %s, use --http-version 2 or unset HTTPS_PROXY", proxy)
}

// h3Failure describes a failed HTTP/3 request. err can carry text from the
// server, such as certificate names, so it is sanitized like the host.
func h3Failure(host string, err error) string {
	return fmt.Sprintf("HTTP/3 to %s failed: %s", output.Sanitize(host), sanitizedError{err})
}

func (t *protoTransport) setState(host, state string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.hosts[host] = state
}

func (t *protoTransport) observe(host string, resp *http.Response) {
	t.mu.Lock()
	defer t.mu.Unlock()
	o := t.observed[host]
	if o == nil {
		o = &observation{}
		t.observed[host] = o
	}
	if !slices.Contains(o.protocols, resp.Proto) {
		o.protocols = append(o.protocols, resp.Proto)
	}
	o.tls = resp.TLS
}

// resetObserved forgets what host's earlier responses ran over.
func (t *protoTransport) resetObserved(host string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.observed, host)
}

// observedOn returns the protocols host's responses ran over since the last
// resetObserved, joined with "+" if auto mode switched midway, and the TLS
// state of the last one. ok is false if there was no response.
func (t *protoTransport) observedOn(host string) (protocol string, state *tls.ConnectionState, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	o := t.observed[host]
	if o == nil {
		return "", nil, false
	}
	return strings.Join(o.protocols, "+"), o.tls, true
}

// advertisesH3 reports whether resp offers HTTP/3 on the same host and port.
func advertisesH3(resp *http.Response, u *url.URL) bool {
	port := u.Port()
	if port == "" {
		port = "443"
	}
	for _, alt := range parseAltSvc(resp.Header.Values("Alt-Svc")) {
		if alt.protocol == "h3" && (alt.host == "" || strings.EqualFold(alt.host, u.Hostname())) && alt.port == port {
			return true
		}
	}
	return false
}

type altSvc struct {
	protocol, host, port string
}

// parseAltSvc parses Alt-Svc header values (RFC 7838). It returns no
// alternatives if any value is "clear", and skips the ones withdrawn with
// ma=0 and the malformed ones.
func parseAltSvc(values []string) []altSvc {
	var alts []altSvc
	for _, v := range values {
		for _, entry := range splitUnquoted(v, ',') {
			entry = strings.TrimSpace(entry)
			// RFC 7838 spells it in lower case; any case is taken here, as
			// staying on TCP is the safe reading
			if strings.EqualFold(entry, "clear") {
				return nil
			}
			params := splitUnquoted(entry, ';')
			protocol, authority, ok := strings.Cut(params[0], "=")
			if !ok {
				continue
			}
			authority, err := strconv.Unquote(strings.TrimSpace(authority))
			if err != nil {
				continue
			}
			host, port, err := net.SplitHostPort(authority)
			if err != nil {
				continue
			}
			if p, err := strconv.ParseUint(port, 10, 16); err != nil || p == 0 {
				continue
			}
			withdrawn := false
			for _, param := range params[1:] {
				name, value, _ := strings.Cut(strings.TrimSpace(param), "=")
				if !strings.EqualFold(strings.TrimSpace(name), "ma") {
					continue
				}
				value = strings.TrimSpace(value)
				if unquoted, err := strconv.Unquote(value); err == nil {
					value = unquoted
				}
				// delta-seconds: ma=00 withdraws the alternative like ma=0
				if ma, err := strconv.ParseUint(value, 10, 64); err == nil && ma == 0 {
					withdrawn = true
				}
			}
			if !withdrawn {
				alts = append(alts, altSvc{strings.TrimSpace(protocol), host, port})
			}
		}
	}
	return alts
}

// splitUnquoted splits s at each sep outside a quoted string, so a quoted
// parameter value cannot end an entry or start a new parameter.
func splitUnquoted(s string, sep byte) []string {
	var parts []string
	quoted, escaped, start := false, false, 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case escaped:
			escaped = false
		case quoted && c == '\\':
			escaped = true
		case c == '"':
			quoted = !quoted
		case !quoted && c == sep:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}

func replayable(req *http.Request) bool {
	return req.Method == http.MethodGet && (req.Body == nil || req.Body == http.NoBody)
}

// dialQUIC opens a QUIC connection on its own UDP socket, bound like the TCP
// dialer's sockets (source address, interface, fwmark, IP family).
func dialQUIC(ctx context.Context, network string, dialer *net.Dialer, addr string, tlsConf *tls.Config, quicConf *quic.Config) (*quic.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, network, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no address found for %s", host)
	}
	return dialQUICAddrs(ctx, ips, port, dialer, tlsConf, quicConf)
}

// dialQUICAddrs tries each address in turn. With a source address it skips
// the addresses of the other family, which that source cannot reach.
func dialQUICAddrs(ctx context.Context, ips []net.IP, port string, dialer *net.Dialer, tlsConf *tls.Config, quicConf *quic.Config) (*quic.Conn, error) {
	src, _ := dialer.LocalAddr.(*net.TCPAddr)
	var errs []error
	for _, ip := range ips {
		udp := "udp6"
		if ip.To4() != nil {
			udp = "udp4"
		}
		var laddr *net.UDPAddr
		if src != nil {
			if (src.IP.To4() != nil) != (ip.To4() != nil) {
				errs = append(errs, fmt.Errorf("%s: not reachable from source address %s", ip, src.IP))
				continue
			}
			laddr = &net.UDPAddr{IP: src.IP, Zone: src.Zone}
		}
		raddr, err := net.ResolveUDPAddr(udp, net.JoinHostPort(ip.String(), port))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		pc, err := (&net.ListenConfig{Control: dialer.Control}).ListenPacket(ctx, udp, udpAddrString(laddr))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", ip, err))
			continue
		}
		// quic-go closes only the sockets it opened itself, so pc is closed
		// here after the transport, on both paths
		tr := &quic.Transport{Conn: pc}
		conn, err := tr.DialEarly(ctx, raddr, tlsConf, quicConf)
		if err == nil {
			go func() { <-conn.Context().Done(); tr.Close(); pc.Close() }()
			return conn, nil
		}
		tr.Close()
		pc.Close()
		errs = append(errs, fmt.Errorf("%s: %w", ip, err))
		if ctx.Err() != nil {
			break
		}
	}
	if len(errs) == 0 {
		// never a nil connection with a nil error: http3 would dereference it
		return nil, errors.New("no address to dial")
	}
	return nil, errors.Join(errs...)
}

// udpAddrString formats a local address for ListenPacket, keeping the IPv6
// zone; nil means any address and port.
func udpAddrString(a *net.UDPAddr) string {
	if a == nil {
		return ""
	}
	return a.String()
}
