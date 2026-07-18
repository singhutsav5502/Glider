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

	ln     net.Listener
	mu     sync.Mutex
	closed bool
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
