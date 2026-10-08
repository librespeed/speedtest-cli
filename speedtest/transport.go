package speedtest

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	"github.com/librespeed/speedtest-cli/output"
)

// protoTransport sends requests to the test servers over the HTTP version
// given with --http-version; other requests (server list, telemetry) use base.
// In auto mode a server moves to HTTP/3 once it advertises it via Alt-Svc,
// and back to TCP if QUIC then fails. A forced version never falls back.
type protoTransport struct {
	version string
	base    http.RoundTripper
	tcp     *http.Transport
	h3      *http3.Transport

	mu    sync.Mutex
	hosts map[string]string // test servers: "", "h3" or "tcp" in auto mode
}

func newProtoTransport(version string, tcp *http.Transport, network string, dialer *net.Dialer) (*protoTransport, error) {
	t := &protoTransport{version: version, tcp: tcp, hosts: map[string]string{}}
	switch version {
	case "auto", "3":
	case "1.1":
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
	base := tcp.Clone()
	base.Protocols = nil
	t.base = base
	t.h3 = &http3.Transport{
		TLSClientConfig: tcp.TLSClientConfig.Clone(),
		// quic-go's default of 1280 does not fit the 1280-byte MTU of
		// WireGuard/Tailscale tunnels (quic-go/quic-go#5573)
		QUICConfig: &quic.Config{InitialPacketSize: 1200},
		Dial: func(ctx context.Context, addr string, tlsConf *tls.Config, quicConf *quic.Config) (*quic.Conn, error) {
			return dialQUIC(ctx, network, dialer, addr, tlsConf, quicConf)
		},
	}
	return t, nil
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
	switch {
	case !isServer:
		return t.base.RoundTrip(req)
	case t.version == "3":
		if req.URL.Scheme != "https" {
			return nil, errors.New("HTTP/3 requires https, try --secure")
		}
		resp, err := t.h3.RoundTrip(req)
		if err != nil && req.Context().Err() == nil {
			output.WriteError("HTTP/3 to %s failed: %s\n", output.Sanitize(req.URL.Host), err)
		}
		return resp, err
	case t.version != "auto" || req.URL.Scheme != "https":
		return t.tcp.RoundTrip(req)
	}

	if state == "h3" {
		resp, err := t.h3.RoundTrip(req)
		if err == nil || req.Context().Err() != nil {
			return resp, err
		}
		output.WriteUI("HTTP/3 to %s failed, falling back to TCP: %s\n", output.Sanitize(req.URL.Host), err)
		t.setState(req.URL.Host, "tcp")
		if !replayable(req) {
			return nil, err
		}
		return t.tcp.RoundTrip(req)
	}

	resp, err := t.tcp.RoundTrip(req)
	if err != nil || state != "" || !advertisesH3(resp, req.URL) {
		return resp, err
	}
	output.WriteDebug("%s advertises HTTP/3, switching to it\n", output.Sanitize(req.URL.Host))
	t.setState(req.URL.Host, "h3")
	// repeat the request over HTTP/3 when possible, so what IsUp reports is
	// what the test then runs over
	if !replayable(req) {
		return resp, nil
	}
	resp.Body.Close()
	return t.RoundTrip(req)
}

func (t *protoTransport) setState(host, state string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.hosts[host] = state
}

// advertisesH3 reports whether resp offers HTTP/3 on the same host and port.
func advertisesH3(resp *http.Response, u *url.URL) bool {
	port := u.Port()
	if port == "" {
		port = "443"
	}
	for _, v := range resp.Header.Values("Alt-Svc") {
		if strings.Contains(v, `h3=":`+port+`"`) {
			return true
		}
	}
	return false
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
	laddr := ""
	if src, ok := dialer.LocalAddr.(*net.TCPAddr); ok {
		laddr = net.JoinHostPort(src.IP.String(), "0")
	}
	var errs []error
	for _, ip := range ips {
		udp := "udp6"
		if ip.To4() != nil {
			udp = "udp4"
		}
		raddr, err := net.ResolveUDPAddr(udp, net.JoinHostPort(ip.String(), port))
		if err != nil {
			return nil, err
		}
		pc, err := (&net.ListenConfig{Control: dialer.Control}).ListenPacket(ctx, udp, laddr)
		if err != nil {
			return nil, err
		}
		tr := &quic.Transport{Conn: pc}
		conn, err := tr.DialEarly(ctx, raddr, tlsConf, quicConf)
		if err == nil {
			go func() { <-conn.Context().Done(); tr.Close() }()
			return conn, nil
		}
		tr.Close()
		errs = append(errs, fmt.Errorf("%s: %w", ip, err))
		if ctx.Err() != nil {
			break
		}
	}
	return nil, errors.Join(errs...)
}
