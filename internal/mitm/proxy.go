package mitm

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/glider-ai/glider/internal/cursorrpc"
	"github.com/glider-ai/glider/internal/metrics"
)

// LocalHandler decides whether to fulfill a decrypted request locally.
// Return handled=true and write the full HTTP response to w.
type LocalHandler interface {
	TryHandle(w http.ResponseWriter, r *http.Request) (handled bool, err error)
}

// Proxy is an HTTPS MITM forward proxy.
type Proxy struct {
	Addr            string
	Authority       *Authority
	Hosts           *HostMatcher
	Local           LocalHandler
	DialTimeout     time.Duration
	Log             *slog.Logger
	Metrics         *metrics.Collector
	PassthroughOnly bool // if true, never call Local
	// TLSClientConfig is used when dialing the real upstream (tests may set InsecureSkipVerify).
	TLSClientConfig *tls.Config
	// Debug optionally peeks RunSSE (and similar) response bodies during origin passthrough.
	Debug *AgentRPCDebugger

	// Redirector, when set, makes Start also listen on TransparentPort for
	// OS-level redirected connections (see redirector.go, redirector_windows.go
	// and planning/transparent_redirector_design.md) — a second, cooperation-free
	// ingress alongside the CONNECT-based one, converging on the same
	// mitmSession/blindTunnel engine.
	Redirector       TransparentRedirector
	TransparentPort  int
	TransparentPorts []int // destination ports to intercept; defaults to []int{443}
	// TransparentAllowProcessNames further scopes interception to only
	// connections owned by one of these process image basenames (e.g.
	// "claude.exe") — see RedirectConfig.AllowProcessNames for why this
	// matters beyond IP/port scoping alone.
	TransparentAllowProcessNames []string

	ln       net.Listener
	transpLn net.Listener
	mu       sync.Mutex
	closed   bool
}

// Start begins listening for CONNECT (and plain HTTP proxy) requests.
func (p *Proxy) Start() error {
	if p.DialTimeout <= 0 {
		p.DialTimeout = 30 * time.Second
	}
	if p.Log == nil {
		p.Log = slog.Default()
	}
	ln, err := net.Listen("tcp", p.Addr)
	if err != nil {
		return err
	}
	p.ln = ln
	go p.serve()

	if p.Redirector != nil && p.TransparentPort != 0 {
		// 0.0.0.0, not 127.0.0.1: the redirector rewrites destinations to the
		// machine's real local IP (not loopback — see redirector_windows.go's
		// detectPrimaryLocalIP for why), so this listener must accept on that
		// interface too, not just loopback.
		transpLn, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", p.TransparentPort))
		if err != nil {
			return fmt.Errorf("mitm: transparent listener: %w", err)
		}
		p.transpLn = transpLn
		go p.serveTransparent()

		ports := p.TransparentPorts
		if len(ports) == 0 {
			ports = []int{443}
		}
		if err := p.Redirector.Start(context.Background(), RedirectConfig{
			ListenPort:        p.TransparentPort,
			MatchPorts:        ports,
			AllowHosts:        p.Hosts.Patterns(),
			AllowProcessNames: p.TransparentAllowProcessNames,
		}); err != nil {
			return fmt.Errorf("mitm: transparent redirector: %w", err)
		}
	}
	return nil
}

// ListenAddr returns the bound address.
func (p *Proxy) ListenAddr() string {
	if p.ln == nil {
		return p.Addr
	}
	return p.ln.Addr().String()
}

// Shutdown stops the listener.
func (p *Proxy) Shutdown(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	if p.Redirector != nil {
		if err := p.Redirector.Stop(); err != nil {
			p.Log.Warn("mitm redirector stop error", "err", err)
		}
	}
	if p.transpLn != nil {
		_ = p.transpLn.Close()
	}
	if p.ln != nil {
		return p.ln.Close()
	}
	return nil
}

func (p *Proxy) serve() {
	for {
		conn, err := p.ln.Accept()
		if err != nil {
			p.mu.Lock()
			closed := p.closed
			p.mu.Unlock()
			if closed || errors.Is(err, net.ErrClosed) {
				return
			}
			p.Log.Warn("mitm accept error", "err", err)
			continue
		}
		go p.handleConn(conn)
	}
}

func (p *Proxy) serveTransparent() {
	for {
		conn, err := p.transpLn.Accept()
		if err != nil {
			p.mu.Lock()
			closed := p.closed
			p.mu.Unlock()
			if closed || errors.Is(err, net.ErrClosed) {
				return
			}
			p.Log.Warn("mitm transparent accept error", "err", err)
			continue
		}
		go p.handleTransparent(conn)
	}
}

// handleTransparent is the OS-level-redirect counterpart of handleCONNECT:
// same destination decision (matchHostPattern → mitmSession/blindTunnel),
// reached without any CONNECT request because the connection didn't arrive
// via an HTTP proxy at all — the OS's own TCP stack was redirected into this
// listener by p.Redirector (see redirector.go). Two things this path needs
// that handleCONNECT gets for free from the request line: the hostname
// (peeked from the TLS ClientHello's SNI, since packet-level redirection
// never sees DNS) and the original destination (p.Redirector.
// ResolveOriginalDestination, used to dial the real IP directly for
// non-allowlisted traffic rather than re-resolving DNS for the SNI name).
func (p *Proxy) handleTransparent(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(p.DialTimeout))

	origHost, origPort, resolveErr := "", 0, error(nil)
	if p.Redirector != nil {
		if resolver, ok := p.Redirector.(OriginResolver); ok {
			origHost, origPort, resolveErr = resolver.ResolveOriginalDestination(conn)
		}
	}

	// bufio.NewReader's default 4096-byte buffer would make a blind
	// Peek(4096) block until either 4096 bytes arrive or the deadline hits —
	// a real TLS client sends one ClientHello record then waits for the
	// server, so a fixed-size peek routinely stalls every connection for
	// the full DialTimeout. Peek the 5-byte TLS record header first to learn
	// the actual record length, then peek exactly that many bytes.
	br := bufio.NewReaderSize(conn, 8192)
	header, headerErr := br.Peek(5)
	if headerErr != nil {
		return
	}
	recLen := int(header[3])<<8 | int(header[4])
	want := 5 + recLen
	if want > 8192 {
		want = 8192
	}
	peeked, peekErr := br.Peek(want)
	if len(peeked) == 0 && peekErr != nil {
		return
	}
	sni, sniErr := peekClientHelloSNI(peeked)

	host := sni
	if sniErr != nil {
		if resolveErr != nil {
			p.Log.Debug("mitm transparent: no SNI and no original-destination record", "sni_err", sniErr, "resolve_err", resolveErr)
			return
		}
		host = origHost
	}

	hostport := host
	if origPort != 0 {
		hostport = net.JoinHostPort(host, fmt.Sprintf("%d", origPort))
	} else if !strings.Contains(hostport, ":") {
		hostport = net.JoinHostPort(host, "443")
	}
	_ = conn.SetDeadline(time.Time{})

	if p.Authority != nil && p.Hosts != nil && p.Hosts.Match(host) {
		p.Log.Debug("mitm decrypt transparent", "host", host, "sni", sni, "resolved_dest", fmt.Sprintf("%s:%d", origHost, origPort))
		if p.Metrics != nil {
			p.Metrics.IncAction("mitm", "decrypt")
		}
		p.mitmSession(conn, br, host, hostport)
		return
	}

	// Not allowlisted: blind-tunnel to the real destination. Prefer the
	// resolved original IP (guaranteed to hit the same server the client
	// meant, no fresh DNS lookup needed) over re-resolving the SNI hostname.
	dialTarget := hostport
	if resolveErr == nil && origHost != "" {
		dialTarget = net.JoinHostPort(origHost, fmt.Sprintf("%d", origPort))
	}
	p.Log.Debug("mitm blind tunnel transparent", "host", host, "dial", dialTarget)
	if p.Metrics != nil {
		p.Metrics.IncAction("mitm", "blind_tunnel")
	}
	p.blindTunnel(conn, br, dialTarget)
}

func (p *Proxy) handleConn(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(p.DialTimeout))
	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	if req.Method == http.MethodConnect {
		p.handleCONNECT(conn, br, req)
		return
	}
	// Plain HTTP proxy (rare for Cursor); forward without MITM.
	p.forwardPlainHTTP(conn, req)
}

func (p *Proxy) handleCONNECT(client net.Conn, br *bufio.Reader, req *http.Request) {
	hostport := req.Host
	if hostport == "" {
		hostport = req.URL.Host
	}
	host := stripPort(hostport)
	if !strings.Contains(hostport, ":") {
		hostport = net.JoinHostPort(host, "443")
	}

	// Acknowledge CONNECT.
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	_ = client.SetDeadline(time.Time{})

	if p.Authority != nil && p.Hosts != nil && p.Hosts.Match(host) {
		// CONNECT opens are tunnel lifecycle, not LLM requests — Debug only; no request-log row.
		p.Log.Debug("mitm decrypt CONNECT", "host", host, "addr", hostport)
		if p.Metrics != nil {
			p.Metrics.IncAction("mitm", "decrypt")
		}
		p.mitmSession(client, br, host, hostport)
		return
	}
	// Blind tunnel for non-allowlisted hosts.
	p.Log.Debug("mitm blind tunnel CONNECT", "host", host, "addr", hostport)
	if p.Metrics != nil {
		p.Metrics.IncAction("mitm", "blind_tunnel")
	}
	p.blindTunnel(client, br, hostport)
}

func (p *Proxy) blindTunnel(client net.Conn, br *bufio.Reader, hostport string) {
	upstream, err := net.DialTimeout("tcp", hostport, p.DialTimeout)
	if err != nil {
		p.Log.Debug("mitm tunnel dial failed", "host", hostport, "err", err)
		return
	}
	defer upstream.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(upstream, br)
		_ = closeWrite(upstream)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(client, upstream)
		_ = closeWrite(client)
	}()
	wg.Wait()
}

func closeWrite(c net.Conn) error {
	type closeWriter interface {
		CloseWrite() error
	}
	if cw, ok := c.(closeWriter); ok {
		return cw.CloseWrite()
	}
	return nil
}

func (p *Proxy) mitmSession(client net.Conn, br *bufio.Reader, host, hostport string) {
	leaf, err := p.Authority.CertificateForHost(host)
	if err != nil {
		p.Log.Error("mitm leaf cert failed", "host", host, "err", err)
		return
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{*leaf},
		MinVersion:   tls.VersionTLS12,
	}
	// Peek buffered bytes + remaining client into TLS server.
	tlsClient := tls.Server(&bufConn{Conn: client, r: br}, tlsCfg)
	if err := tlsClient.Handshake(); err != nil {
		p.Log.Debug("mitm client handshake failed", "host", host, "err", err)
		return
	}
	defer tlsClient.Close()

	connectSession := fmt.Sprintf("c%x", time.Now().UnixNano())
	p.Log.Debug("mitm decrypt session", "host", host, "connect_session", connectSession)

	for {
		_ = tlsClient.SetDeadline(time.Now().Add(p.DialTimeout))
		req, err := http.ReadRequest(bufio.NewReader(tlsClient))
		if err != nil {
			return
		}
		_ = tlsClient.SetDeadline(time.Time{})
		req = req.WithContext(WithConnectSession(req.Context(), connectSession))

		if !p.PassthroughOnly && p.Local != nil {
			rw := &responseCapture{conn: tlsClient, header: http.Header{}}
			handled, herr := p.Local.TryHandle(rw, req)
			if herr != nil {
				p.Log.Warn("mitm local handler error", "err", herr)
			}
			if handled {
				if !rw.wrote {
					http.Error(rw, "empty local response", http.StatusBadGateway)
				}
				if req.Close || rw.header.Get("Connection") == "close" {
					return
				}
				continue
			}
			// Not handled — fall through to origin passthrough. Body may have been consumed;
			// LocalHandler must not consume body unless it handles.
		}

		if err := p.passthroughHTTPS(tlsClient, req, host, hostport); err != nil {
			p.Log.Debug("mitm passthrough failed", "host", host, "err", err)
			return
		}
		if req.Close {
			return
		}
	}
}

func (p *Proxy) passthroughHTTPS(client net.Conn, req *http.Request, host, hostport string) error {
	raw, err := net.DialTimeout("tcp", hostport, p.DialTimeout)
	if err != nil {
		return err
	}
	defer raw.Close()
	cfg := &tls.Config{
		ServerName: host,
		MinVersion: tls.VersionTLS12,
	}
	if p.TLSClientConfig != nil {
		cfg = p.TLSClientConfig.Clone()
		if cfg.ServerName == "" {
			cfg.ServerName = host
		}
	}
	up := tls.Client(raw, cfg)
	if err := up.Handshake(); err != nil {
		return err
	}
	defer up.Close()

	// Ensure Host header and URL are absolute for upstream.
	req.RequestURI = ""
	if req.URL.Scheme == "" {
		req.URL.Scheme = "https"
	}
	if req.URL.Host == "" {
		req.URL.Host = host
	}
	req.Host = host
	if err := req.Write(up); err != nil {
		return err
	}
	resp, err := http.ReadResponse(bufio.NewReader(up), req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if p.Debug != nil && p.Debug.Enabled {
		resp.Body = wrapResponsePeek(resp.Body, req, resp, p.Debug)
	}
	if err := resp.Write(client); err != nil {
		return err
	}
	return nil
}

func (p *Proxy) forwardPlainHTTP(client net.Conn, req *http.Request) {
	hostport := req.Host
	if hostport == "" {
		hostport = req.URL.Host
	}
	if !strings.Contains(hostport, ":") {
		hostport = net.JoinHostPort(stripPort(hostport), "80")
	}
	up, err := net.DialTimeout("tcp", hostport, p.DialTimeout)
	if err != nil {
		_, _ = io.WriteString(client, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	defer up.Close()
	req.RequestURI = ""
	if err := req.Write(up); err != nil {
		return
	}
	resp, err := http.ReadResponse(bufio.NewReader(up), req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	_ = resp.Write(client)
}

// bufConn reads first from buffered reader then from Conn.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufConn) Read(p []byte) (int, error) {
	if b.r != nil && b.r.Buffered() > 0 {
		return b.r.Read(p)
	}
	return b.Conn.Read(p)
}

// responseCapture implements http.ResponseWriter over a raw TLS conn.
type responseCapture struct {
	conn        net.Conn
	header      http.Header
	status      int
	wroteHeader bool
	wrote       bool
}

func (r *responseCapture) Header() http.Header { return r.header }

func (r *responseCapture) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status = status
	r.wroteHeader = true
	if r.status == 0 {
		r.status = http.StatusOK
	}
	var b strings.Builder
	fmt.Fprintf(&b, "HTTP/1.1 %d %s\r\n", r.status, http.StatusText(r.status))
	_ = r.header.Write(&b)
	b.WriteString("\r\n")
	_, _ = io.WriteString(r.conn, b.String())
	r.wrote = true
}

func (r *responseCapture) Write(p []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	r.wrote = true
	return r.conn.Write(p)
}

// Ensure responseCapture implements http.Flusher for SSE.
func (r *responseCapture) Flush() {}

// wrapResponsePeek tees the first N response body bytes for debug ObserveResponse
// without blocking the client copy. Fires once when peek fills or on Close.
func wrapResponsePeek(body io.ReadCloser, req *http.Request, resp *http.Response, dbg *AgentRPCDebugger) io.ReadCloser {
	if body == nil || dbg == nil || !dbg.Enabled || req == nil {
		return body
	}
	path := ""
	if req.URL != nil {
		path = req.URL.Path
	}
	// Only peek Agent-ish response streams (RunSSE is the critical one).
	if !cursorrpc.IsRunSSEPath(path) && ClassifyPath(path) != PathAgentRPC {
		return body
	}
	return &responsePeekCloser{
		r:    body,
		max:  cursorrpc.MaxRunSSEResponsePeek,
		req:  req,
		resp: resp,
		dbg:  dbg,
		buf:  &bytes.Buffer{},
	}
}

type responsePeekCloser struct {
	r     io.ReadCloser
	max   int
	req   *http.Request
	resp  *http.Response
	dbg   *AgentRPCDebugger
	buf   *bytes.Buffer
	fired bool
	mu    sync.Mutex
}

func (p *responsePeekCloser) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.mu.Lock()
		if p.buf.Len() < p.max {
			remain := p.max - p.buf.Len()
			if remain > n {
				remain = n
			}
			_, _ = p.buf.Write(b[:remain])
			if p.buf.Len() >= p.max {
				p.fireLocked()
			}
		}
		p.mu.Unlock()
	}
	return n, err
}

func (p *responsePeekCloser) Close() error {
	p.mu.Lock()
	p.fireLocked()
	p.mu.Unlock()
	return p.r.Close()
}

func (p *responsePeekCloser) fireLocked() {
	if p.fired || p.dbg == nil {
		return
	}
	p.fired = true
	peek := append([]byte(nil), p.buf.Bytes()...)
	status := 0
	ct := ""
	if p.resp != nil {
		status = p.resp.StatusCode
		ct = p.resp.Header.Get("Content-Type")
	}
	p.dbg.ObserveResponse(p.req, status, ct, peek)
}
