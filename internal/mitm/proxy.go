package mitm

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
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
	// The proxy uses TLSClientConfig when it dials the true upstream server.
	// A test can set InsecureSkipVerify.
	TLSClientConfig *tls.Config
	// Debug can examine RunSSE response bodies, and similar bodies, during
	// passthrough to the origin. It is optional.
	Debug *AgentRPCDebugger

	// Set Redirector to make Start listen on TransparentPort also. That port
	// accepts the connections that the operating system redirects. Refer to
	// redirector.go, redirector_windows.go and
	// planning/transparent_redirector_design.md. This is a second entry point
	// next to the CONNECT entry point, and it needs no help from the client.
	// Both entry points use the same mitmSession and blindTunnel code.
	Redirector       TransparentRedirector
	TransparentPort  int
	TransparentPorts []int // destination ports to intercept; defaults to []int{443}
	// TransparentAllowProcessNames makes the interception more narrow. Glider
	// then intercepts only the connections that these processes own. Give the
	// base name of the process image, for example "claude.exe". Refer to
	// RedirectConfig.AllowProcessNames for the cause. A limit on the IP
	// address and the port alone is not sufficient.
	TransparentAllowProcessNames []string

	ln       net.Listener
	transpLn net.Listener
	mu       sync.Mutex
	closed   bool

	// passthroughTransport is one http.Transport. It has a long life, and
	// each passthroughHTTPS call uses it. The earlier design made a new
	// Transport for each call. Refer to the comment on
	// sharedPassthroughTransport for the cause of the change.
	passthroughTransport     *http.Transport
	passthroughTransportOnce sync.Once
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
		// Transparent interception is an addition to the CONNECT proxy that
		// listens above. It is OPTIONAL. Therefore a failure here must not
		// stop the full application.
		//
		// Corrected 2026-07-28 after a report from a user. A person started
		// glider.exe with a double click and with no elevation. The process
		// stopped immediately, and it showed no tray icon and no error. The
		// cause was here: this code returned an error to main.go, and
		// main.go called os.Exit(1). That stopped a process which had
		// already started its gateway and its dashboard correctly. The
		// failure was in a function that the user possibly did not know
		// about, because mitm.transparent is true by default.
		//
		// The usual cause is WinDivertOpen. It needs Administrator
		// permission to load its kernel driver. A double click in Explorer
		// never gives that permission, but a terminal that is already
		// elevated does give it. This agrees with the report: "it operates
		// from cmd, but not from a double click".
		//
		// Use 0.0.0.0 and not 127.0.0.1. The redirector writes the true
		// local IP address of the machine as the destination, and not the
		// loopback address. Refer to detectPrimaryLocalIP in
		// redirector_windows.go for the cause. Therefore this listener must
		// accept on that interface also, and not only on loopback.
		transpLn, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", p.TransparentPort))
		if err != nil {
			p.Log.Warn("mitm: transparent listener failed to bind — continuing without OS-level transparent "+
				"interception (the gateway and CONNECT-based MITM proxy still work normally)", "err", err)
			return nil
		}

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
			p.Log.Warn("mitm: transparent redirector failed to start — continuing without OS-level transparent "+
				"interception (the gateway and CONNECT-based MITM proxy still work normally); if this is "+
				"WinDivertOpen failing, run Glider as Administrator to enable it", "err", err)
			_ = transpLn.Close()
			return nil
		}
		// Set p.transpLn and start the Accept loop of serveTransparent only
		// after the redirector starts correctly.
		//
		// This was a true defect, and
		// TestProxyStart_TransparentRedirectorFailureIsNonFatal found it on
		// 2026-07-29. The earlier version of this function started
		// serveTransparent() BEFORE it knew the result of Redirector.Start.
		// Then it set p.transpLn to nil after a failure. That write had no
		// synchronization, and serveTransparent read the same field at the
		// same time. The result was a live panic on a nil pointer in the
		// Accept() call of that goroutine.
		//
		// This sequence removes the race fully. No code reads p.transpLn
		// before this line, and no code sets the field to nil after this
		// line while Start operates.
		p.transpLn = transpLn
		go p.serveTransparent()
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
	if p.passthroughTransport != nil {
		p.passthroughTransport.CloseIdleConnections()
	}
	if p.ln != nil {
		return p.ln.Close()
	}
	return nil
}

// sharedPassthroughTransport returns the one http.Transport that each
// passthroughHTTPS call uses again. It does NOT make a new Transport for each
// call. That was the design until a live test showed a problem on 2026-07-29.
//
// A new dial and a new TLS handshake for each request add a delay. The delay
// is large and it changes. It is usually less than 200ms, but sometimes it is
// much more. A client that speaks directly to the same origin does not have
// this delay, because it uses its own warm connection again.
//
// This difference was the last possible cause of a live problem: a true
// client (cursor-agent) sometimes stopped a plain passthrough. The same
// investigation removed or corrected each more simple cause first. Those
// causes were a read of the request body that blocked, congestion in the
// WinDivert packet queue, and a defect that cut the body in the fall-through
// path.
//
// The missing connection reuse is not a regression from those corrections. A
// person verified this first. That person built the last commit before the
// regression, and tested it live with the current and correct config. It
// failed in the same way. The first version of this function in this
// repository had the connection reuse. No person removed it later.
//
// The http.Transport of Go keeps a pool of connections (MaxIdleConnsPerHost,
// IdleConnTimeout and others). The old code lost all of that. For each
// request it made a new Transport, used the Transport one time, and then
// called CloseIdleConnections on it immediately. The correct method is this
// one. Make the Transport one time. Use it again from many goroutines, which
// is safe by design. Then let it control its own pool.
//
// TLSClientConfig here has no fixed ServerName. This is on purpose. The old
// config for each call set cfg.ServerName to the host for that one call. The
// Transport of Go finds the correct SNI for each request from the destination
// host of that request. This is what lets ONE shared Transport keep correct
// pools of connections to many different origins at the same time (cursor.sh,
// anthropic.com, googleapis.com and others). This is the method that
// Transport is made for. sync.Once makes the first initialization safe when
// many passthroughHTTPS calls operate at the same time, and it does not need
// p.mu.
func (p *Proxy) sharedPassthroughTransport() *http.Transport {
	p.passthroughTransportOnce.Do(func() {
		cfg := &tls.Config{MinVersion: tls.VersionTLS12}
		if p.TLSClientConfig != nil {
			cfg = p.TLSClientConfig.Clone()
		}
		p.passthroughTransport = &http.Transport{
			TLSClientConfig:     cfg,
			ForceAttemptHTTP2:   true,
			DialContext:         (&net.Dialer{Timeout: p.DialTimeout}).DialContext,
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
		}
	})
	return p.passthroughTransport
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

// handleTransparent does the same work as handleCONNECT, but for a
// connection that the operating system redirects. It makes the same
// decision about the destination (matchHostPattern, then mitmSession or
// blindTunnel). There is no CONNECT request, because the connection did not
// come through an HTTP proxy. p.Redirector redirected the TCP stack of the
// operating system into this listener. Refer to redirector.go.
//
// This path must find two items that the request line gives to
// handleCONNECT with no work:
//   - The host name. This path reads it from the SNI field of the TLS
//     ClientHello, because redirection at the packet level never sees DNS.
//   - The original destination, from
//     p.Redirector.ResolveOriginalDestination. This path dials the true IP
//     address directly for the traffic that is not on the allowlist. It
//     does not resolve the SNI name in DNS again.
func (p *Proxy) handleTransparent(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(p.DialTimeout))

	origHost, origPort, resolveErr := "", 0, error(nil)
	if p.Redirector != nil {
		if resolver, ok := p.Redirector.(OriginResolver); ok {
			origHost, origPort, resolveErr = resolver.ResolveOriginalDestination(conn)
		}
	}

	// ProcessFilter does the AllowProcessNames test that Windows does at the
	// packet level. It operates here instead. Refer to the comment on
	// ProcessFilter for the cause: a redirector that uses iptables REDIRECT
	// on Linux needs this step after the accept. WinDivertRedirector does
	// not implement this interface, because on Windows a connection that is
	// not allowed never arrives at this listener.
	//
	// A connection that is not allowed has already completed a true TCP
	// handshake against the socket of Glider. Thus there is no packet to
	// "inject again with no change", as there is on Windows. The equivalent
	// action is a blind tunnel to the true destination with no test. This
	// does no MITM and does not use the host allowlist. It is the same as
	// the "not on the allowlist" path below.
	if p.Redirector != nil {
		if filter, ok := p.Redirector.(ProcessFilter); ok && !filter.ConnectionAllowed(conn) {
			if resolveErr != nil || origHost == "" {
				p.Log.Debug("mitm transparent: process not allowed and original destination unresolvable, dropping", "resolve_err", resolveErr)
				return
			}
			p.Log.Debug("mitm transparent: process not allowed, blind-tunneling unconditionally", "dial", net.JoinHostPort(origHost, fmt.Sprintf("%d", origPort)))
			if p.Metrics != nil {
				p.Metrics.IncAction("mitm", "blind_tunnel")
			}
			p.blindTunnel(conn, bufio.NewReaderSize(conn, 8192), net.JoinHostPort(origHost, fmt.Sprintf("%d", origPort)))
			return
		}
	}

	// The default buffer of bufio.NewReader holds 4096 bytes. Thus a
	// Peek(4096) blocks until 4096 bytes arrive or the deadline ends. A true
	// TLS client sends one ClientHello record and then waits for the server.
	// Therefore a peek of a fixed size stops each connection for the full
	// DialTimeout. Peek the TLS record header of 5 bytes first to find the
	// true length of the record. Then peek exactly that quantity of bytes.
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

// mitmSession ends TLS for one decrypted connection. It then serves each
// request on that connection through the ServeTLS function of
// net/http.Server. ServeTLS agrees the ALPN protocol with the true client
// automatically, either h2 or http/1.1, and it sends both to the same
// http.Handler.
//
// An earlier version used a loop of tls.Server() and http.ReadRequest. That
// loop gave no ALPN, and it always assumed the framing of HTTP/1.1.
//
// Corrected 2026-07-28. Some clients speak only h2. This is confirmed live:
// the true completion-plane host of cursor-agent,
// agentn.global.api5.cursor.sh, always agrees h2. Refer to the entry
// "cursor-agent exact completion-plane RPC name" in
// planning/agent_cli_interop.md. Such a client cannot complete a handshake
// against a server that gives no ALPN. Or a reader that speaks only HTTP/1.1
// damages its stream. A person then tested the true wire behaviour of
// cursor-agent. After that test, this became a true defect in the transparent
// interception of Glider. It was not only a theoretical defect.
func (p *Proxy) mitmSession(client net.Conn, br *bufio.Reader, host, hostport string) {
	leaf, err := p.Authority.CertificateForHost(host)
	if err != nil {
		p.Log.Error("mitm leaf cert failed", "host", host, "err", err)
		return
	}

	connectSession := fmt.Sprintf("c%x", time.Now().UnixNano())
	p.Log.Debug("mitm decrypt session", "host", host, "connect_session", connectSession)

	ln := newSingleConnListener(&bufConn{Conn: client, r: br})
	srv := &http.Server{
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{*leaf},
			MinVersion:   tls.VersionTLS12,
		},
		ReadHeaderTimeout: p.DialTimeout,
		IdleTimeout:       p.DialTimeout,
		ErrorLog:          discardHTTPServerLog,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.WithContext(WithConnectSession(r.Context(), connectSession))
			tw := &trackedResponseWriter{ResponseWriter: w}

			if !p.PassthroughOnly && p.Local != nil {
				handled, herr := p.Local.TryHandle(tw, r)
				if herr != nil {
					p.Log.Warn("mitm local handler error", "err", herr)
				}
				if handled {
					if !tw.wrote {
						http.Error(tw, "empty local response", http.StatusBadGateway)
					}
					return
				}
				// The local handler did not handle the request. Continue to
				// the passthrough to the origin. The handler can have read
				// the body. The contract does not change: LocalHandler must
				// not read the body unless it handles the request.
			}

			if err := p.passthroughHTTPS(tw, r, host, hostport); err != nil {
				p.Log.Debug("mitm passthrough failed", "host", host, "err", err)
			}
		}),
	}
	_ = srv.ServeTLS(ln, "", "")
}

// discardHTTPServerLog stops the default stderr log of http.Server. That
// log gives messages such as "http: TLS handshake error..." when a client
// stops in the middle of a handshake. This is usual on a MITM proxy. Proxy
// already writes to p.Log at each call site of importance.
var discardHTTPServerLog = log.New(io.Discard, "", 0)

// trackedResponseWriter records if the code wrote any data. Thus the
// handler in mitmSession can find the difference between two conditions.
// The first condition is a defect in LocalHandler: it returns handled=true,
// but it writes nothing. The second condition is a correct response that
// has headers and no body. The old responseCapture.wrote field found the
// same difference.
//
// trackedResponseWriter sends Flush() to the Flusher of the true
// http.ResponseWriter. net/http.Server gives that Flusher for h1.1 and for
// h2. Thus SSE replies continue to operate with no change. Glider sends the
// text of a delegate in small parts in such a reply.
type trackedResponseWriter struct {
	http.ResponseWriter
	wrote bool
}

func (t *trackedResponseWriter) WriteHeader(status int) {
	t.wrote = true
	t.ResponseWriter.WriteHeader(status)
}

func (t *trackedResponseWriter) Write(p []byte) (int, error) {
	t.wrote = true
	return t.ResponseWriter.Write(p)
}

func (t *trackedResponseWriter) Flush() {
	if f, ok := t.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// passthroughHTTPS sends req to the true origin on a true TLS connection.
// That connection offers h2 and http/1.1 in its ALPN list. The function then
// writes the response of the origin back through w.
//
// It uses http.Transport. The version before 2026-07-28 used a manual
// sequence of tls.Client, req.Write and http.ReadResponse. The transport of
// net/http already agrees the ALPN protocol with the true origin. It also
// makes the correct response framing for both versions of the protocol. The
// manual version always assumed HTTP/1.1, and it failed against each origin
// that speaks only HTTP/2. The comment on mitmSession gives the example from
// the live test.
func (p *Proxy) passthroughHTTPS(w http.ResponseWriter, req *http.Request, host, hostport string) error {
	// Use sharedPassthroughTransport, and do not make a new Transport for
	// each call. Refer to the comment on that function for the full cause
	// and the live investigation. ServerName is not set for each call now.
	// This is on purpose. The shared Transport finds the correct value for
	// each request.
	transport := p.sharedPassthroughTransport()

	// Use an independent context. Do NOT use req.Context().
	//
	// This was the root cause of a problem that was difficult to find. The
	// connection stopped for approximately 29 to 30 seconds. Then it gave
	// "context canceled" (2026-07-30). Instruments from net/http/httptrace
	// showed that the block was in io.Copy(w, resp.Body). The block was not in
	// the dial or in the write of the request, and those two steps complete in
	// much less than one second.
	//
	// req.Context() is the context of the OWN inbound connection of
	// cursor-agent. The true client of cursor-agent sends RST_STREAM(CANCEL) on
	// its own request stream almost immediately after it sends the request. An
	// isolated HTTP/2 frame trace from wirecapture confirmed this earlier in the
	// same investigation. The client does this independently of the speed of the
	// response of Glider.
	//
	// Clone the outbound request with that same context, and the early
	// self-cancellation of cursor-agent then stops the full relay of Glider to
	// the true origin. This includes the copy of the response body. The relay
	// stops even when it operates correctly and would complete with no problem.
	//
	// A large, independent time limit lets the outbound part of the connection
	// succeed or fail on its own condition. It does not take a cancellation from
	// the client side. Such a cancellation can have no relation to the ability
	// of the true origin to answer. The value of 120s here is an independent
	// limit for a true network response from a true vendor origin (cursor.sh).
	// It has no relation to vendors.RunTimeout, which is a limit on a local
	// subprocess and has no limit by default now for different causes. An
	// earlier version of this comment said that the two values agree on purpose.
	// That became incorrect when only one of them changed.
	//
	// AgentService/Run gets a much smaller limit. This is from a live incident
	// on 2026-07-31. The independent context above separated the life of the
	// outbound relay from the life of the inbound client. That solved the
	// incorrect cancellations, but it made a new problem. The true client of
	// cursor-agent stops its wait and connects again on its own cycle of
	// approximately 30 seconds. A live test confirmed 4 attempts to connect
	// again, and they were approximately 30 to 34 seconds apart. But each relay
	// that the client left continued here for the FULL 120s, because no signal
	// told the relay that the client had gone.
	//
	// A new counter, inFlightAgentRunRelays, showed that these relays collect:
	// concurrent_in_flight increased 1, 2, 3, then 4 during one true exchange
	// with many reconnections. Thus at the 4th attempt, three relays that the
	// client had left were still active. They used true resources — decrypt
	// workers, TLS negotiation and the network — against the attempt that could
	// succeed. This is a measurable load on exactly the connections of most
	// importance.
	//
	// There is no dependable method to find the condition "the client truly
	// stopped". req.Context() is exactly the method that the independent context
	// above avoids, for the cause above. Therefore this code does not try to
	// find that condition. It only refuses to keep a relay open longer than the
	// wait time of the client can make of use: 40s. That is a small quantity
	// more than the observed cycle of 30 to 34 seconds. Therefore it gives a
	// fair opportunity to a connection that is only slow. It is not the margin
	// of 3 to 4 times that let the abandoned relays collect before.
	relayTimeout := 120 * time.Second
	if cursorrpc.IsAgentServiceRunPath(req.URL.Path) {
		relayTimeout = 40 * time.Second
	}
	outCtx, cancel := context.WithTimeout(context.Background(), relayTimeout)
	defer cancel()
	outReq := req.Clone(outCtx)
	outReq.RequestURI = ""
	if outReq.URL.Scheme == "" {
		outReq.URL.Scheme = "https"
	}
	// Use hostport, and not host. Then the dialer of the Transport resolves
	// and connects to the correct address. The Host field below keeps only
	// the host name for the true HTTP Host header. This agrees with the
	// division in the original request.
	outReq.URL.Host = hostport
	outReq.Host = host

	// Set GetBody, and not only Body. This is a live result of the change to one
	// shared Transport with a pool (2026-07-29). A connection in the pool can
	// become invalid between two uses. The origin can close it, or a NAT or a
	// firewall can remove it. The Transport of Go tries such a failure again on
	// a new connection. But it does this only *if* it can move the request body
	// back to the start. It cannot do that for a plain io.ReadCloser with no
	// GetBody. Then the user sees "net/http: cannot rewind body after connection
	// loss", and not a retry that is not visible.
	//
	// Do this only for a body with a KNOWN length (req.ContentLength >= 0). A
	// body with an unknown length must NOT be read here immediately. The
	// AgentService/Run body of cursor-agent is such a body. Code above makes it
	// again as a live stream. It uses the first envelope from a buffer, and then
	// the bytes that still arrive from the true client. Refer to the comments on
	// DelegateHandler and handleAgentRPC. A read here would block this full call
	// at the speed of the client. That is exactly the blocking read that this
	// investigation corrected one time, and it would only move to a new
	// position.
	//
	// For that condition, Glider sends Body live and does not set GetBody. A
	// connection in the pool that becomes invalid during a relay in two
	// directions is a rare condition. It is not sufficient cause to make a block
	// on each request again.
	if req.Body != nil && req.Body != http.NoBody {
		if req.ContentLength >= 0 {
			bodyBytes, readErr := io.ReadAll(req.Body)
			if readErr != nil {
				return fmt.Errorf("mitm passthrough: read request body: %w", readErr)
			}
			outReq.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			outReq.GetBody = func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(bodyBytes)), nil
			}
			outReq.ContentLength = int64(len(bodyBytes))
		} else {
			outReq.Body = req.Body
			outReq.ContentLength = -1
		}
	}

	resp, err := transport.RoundTrip(outReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if p.Debug != nil && p.Debug.Enabled {
		resp.Body = wrapResponsePeek(resp.Body, req, resp, p.Debug)
	}

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	// Use flushAfterWrite, and not a plain io.Copy(w, ...). This was THE
	// root cause of the cursor-agent stop that was difficult to find. A test
	// on 2026-07-31 gave exact numbers.
	//
	// net/http keeps response writes in an internal buffer
	// (bufferBeforeChunkingSize = 2048 for HTTP/1.1;
	// http2handlerChunkWriteSize = 4096 for HTTP/2). It sends the data only
	// when the buffer becomes full, when the handler returns, or when code
	// calls Flush. This relay called Flush at no position. Therefore a
	// streaming RPC response collected in the buffer and did not go to the
	// client. Such a response is a slow sequence of small frames by design.
	//
	// The live data had two clear groups. Each AgentService/Run stream that
	// stopped went to 116 to 215 bytes and then stopped. That quantity is
	// less than both limits, thus the client saw nothing. The client then
	// reached its own wait time of approximately 30 seconds and connected
	// again. It did this many times, for 160 to 420 seconds. Each stream
	// that gave its data had more than 14KB. That quantity is more than
	// 4096, and the buffer emptied automatically as a result. This is the
	// only cause of the condition that Glider appeared to "operate after a
	// delay".
	//
	// This also gives the cause of two more observations. A person stopped
	// glider.exe, and the same prompt completed immediately, because no
	// proxy with a buffer stayed in the path. And small delegate replies had
	// the largest problem, because they are the smallest responses.
	//
	// The origin adapters of ngl (adapter_*_origin.go) always call Flush for
	// exactly this cause. Only the plain passthrough did not do the
	// equivalent.
	//
	// Put w in a wrapper, and do not give w directly. The wrapper also stops
	// io.Copy from the io.ReaderFrom fast path of net/http. That path does
	// no flush for each write.
	var dst io.Writer = w
	if flusher, ok := w.(http.Flusher); ok {
		dst = flushAfterWrite{w: w, flusher: flusher}
	}
	_, copyErr := io.Copy(dst, resp.Body)
	return copyErr
}

// flushAfterWrite sends each write directly to the client. The data does
// not stay in the internal response buffer of net/http. Refer to the use of
// this type in passthroughHTTPS for the full record of the incident.
//
// This is permanent, and it is not an instrument for diagnosis. Each relay
// of a streaming response fails without it when the frames of the response
// are smaller than the buffer. This includes Connect and gRPC in two
// directions, SSE, and a long poll in parts. For a protocol that uses a
// heartbeat, small frames are the usual condition and not a rare one.
type flushAfterWrite struct {
	w       io.Writer
	flusher http.Flusher
}

func (f flushAfterWrite) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if n > 0 {
		f.flusher.Flush()
	}
	return n, err
}

// singleConnListener changes one net.Conn that the code accepted before
// into the net.Listener interface. http.Server.ServeTLS needs that
// interface. Thus the ALPN negotiation and the request dispatch of ServeTLS
// apply to exactly this one connection that Glider decrypts.
type singleConnListener struct {
	conn   net.Conn
	used   bool
	closed chan struct{}
}

func newSingleConnListener(c net.Conn) *singleConnListener {
	return &singleConnListener{conn: c, closed: make(chan struct{})}
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	if !l.used {
		l.used = true
		return l.conn, nil
	}
	<-l.closed
	return nil, io.EOF
}

func (l *singleConnListener) Close() error {
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return nil
}

func (l *singleConnListener) Addr() net.Addr { return l.conn.LocalAddr() }

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
