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

	// passthroughTransport is a single, long-lived, shared http.Transport
	// used by every passthroughHTTPS call — see sharedPassthroughTransport's
	// own doc comment for why this replaced a fresh-Transport-per-call design.
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
		// Transparent interception is a real, but OPTIONAL enhancement on
		// top of the CONNECT-based proxy already listening above — a
		// failure here must never take down the whole app. Fixed
		// 2026-07-28 after a real report: a double-clicked, non-elevated
		// glider.exe vanished instantly with no tray icon and no visible
		// error, because this used to return an error all the way up to
		// main.go's os.Exit(1) — killing a process that had already
		// successfully started its gateway and dashboard, over a failure
		// in a feature the user may not even have known was on (mitm.transparent
		// defaults to true). WinDivertOpen requiring Administrator
		// privileges to load its kernel driver is the overwhelmingly
		// common cause; a plain double-click from Explorer is never
		// elevated, while an already-elevated terminal is — exactly
		// matching the reported "works from cmd, not by double-clicking"
		// split.
		//
		// 0.0.0.0, not 127.0.0.1: the redirector rewrites destinations to the
		// machine's real local IP (not loopback — see redirector_windows.go's
		// detectPrimaryLocalIP for why), so this listener must accept on that
		// interface too, not just loopback.
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
		// Only assign p.transpLn and start serveTransparent's Accept loop
		// once the redirector has actually, successfully armed — real bug,
		// caught by TestProxyStart_TransparentRedirectorFailureIsNonFatal
		// itself (2026-07-29): the earlier version of this function started
		// serveTransparent() BEFORE knowing whether Redirector.Start would
		// succeed, then set p.transpLn = nil on failure — an unsynchronized
		// write racing serveTransparent's own concurrent read of the same
		// field, causing a real, live nil-pointer panic in that goroutine's
		// Accept() call. Ordering it this way removes the race entirely:
		// nothing reads p.transpLn until after this line, and nothing sets
		// it to nil after that point during Start.
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

// sharedPassthroughTransport returns the one http.Transport every
// passthroughHTTPS call reuses — NOT a fresh Transport per call, which was
// the design until a real, live-confirmed issue (2026-07-29): a fresh
// dial+TLS handshake on every single request adds meaningful, variable
// latency (typically under 200ms, but occasionally much more) that a
// client talking directly to the same origin doesn't pay, since it
// naturally reuses its own warm connection — a gap that remained the last
// plausible explanation for a real client (cursor-agent) occasionally
// giving up on plain passthrough after every simpler cause (a blocking
// request-body read, WinDivert packet-queue congestion, a body-truncation
// bug in the fall-through path) was independently ruled out or fixed in
// the same investigation. Verified this wasn't a regression from any of
// those other fixes first: built and live-tested the last pre-regression
// commit with the current, correct config — it failed identically, so the
// missing connection reuse (present in this codebase's very first version
// of this function, never a "removed" feature) is the remaining lead, not
// a fix for something that used to work.
//
// Go's own http.Transport already implements connection pooling
// internally (MaxIdleConnsPerHost, IdleConnTimeout, etc.) — the old code
// discarded that entirely by constructing a brand-new Transport, using it
// once, and immediately calling CloseIdleConnections on it, for every
// single request. This is the standard, idiomatic way to use
// http.Transport: construct once, reuse concurrently (it's safe for
// concurrent use by design), let it manage its own connection pool.
//
// TLSClientConfig here deliberately has no fixed ServerName (unlike the
// old per-call config, which set cfg.ServerName = host for that one call)
// — Go's Transport derives SNI correctly per request from the outbound
// request's own destination host, which is what lets ONE shared Transport
// correctly pool connections to many different origins (cursor.sh,
// anthropic.com, googleapis.com, ...) at once, exactly how Transport is
// designed to be used. sync.Once makes first-use initialization safe
// under concurrent passthroughHTTPS calls without needing p.mu.
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

	// ProcessFilter is Windows' packet-level AllowProcessNames check,
	// relocated to run here instead — see ProcessFilter's own doc comment
	// for why a Linux/iptables-REDIRECT redirector needs this post-accept
	// step at all, unlike WinDivertRedirector (which never implements
	// this interface, since a disallowed connection never reaches this
	// listener in the first place on that platform). A disallowed
	// connection here has already completed a real TCP handshake against
	// Glider's own socket — there's no packet to "reinject unchanged" the
	// way there is on Windows, so the equivalent is an unconditional
	// blind-tunnel to the real destination, bypassing host-allowlist MITM
	// entirely, same as the existing "not allowlisted" path below.
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

// mitmSession terminates TLS for one decrypted connection and serves every
// request on it through net/http.Server's own ServeTLS, which negotiates
// ALPN (h2 vs http/1.1) with the real client automatically and dispatches
// either to the same http.Handler — replacing an earlier hand-rolled
// tls.Server()+http.ReadRequest loop that offered no ALPN at all and
// unconditionally assumed HTTP/1.1 framing. Fixed 2026-07-28: a genuinely
// h2-only client (confirmed live — cursor-agent's real completion-plane
// host, agentn.global.api5.cursor.sh, negotiates h2 unconditionally, see
// planning/agent_cli_interop.md's "cursor-agent exact completion-plane RPC
// name" entry) either can't complete a handshake against a server offering
// no ALPN at all, or has its stream corrupted by an HTTP/1.1-only reader —
// this was a real gap in Glider's own transparent interception, not just a
// theoretical one, once cursor-agent's actual wire behavior was confirmed.
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
				// Not handled — fall through to origin passthrough. Body may
				// have been consumed; LocalHandler must not consume body
				// unless it handles (unchanged contract).
			}

			if err := p.passthroughHTTPS(tw, r, host, hostport); err != nil {
				p.Log.Debug("mitm passthrough failed", "host", host, "err", err)
			}
		}),
	}
	_ = srv.ServeTLS(ln, "", "")
}

// discardHTTPServerLog silences http.Server's own default stderr logging
// (e.g. "http: TLS handshake error..." for a client that hangs up mid
// handshake, routine on a MITM proxy) — Proxy already logs through p.Log at
// the call sites that matter.
var discardHTTPServerLog = log.New(io.Discard, "", 0)

// trackedResponseWriter records whether anything was ever written, so
// mitmSession's handler can tell "LocalHandler claimed handled=true but
// wrote nothing" (a real bug in that handler) apart from a legitimate
// empty-but-headers-only response — the same distinction the old
// responseCapture.wrote field made. Forwards Flush() to the real
// http.ResponseWriter's own Flusher (present for both h1.1 and h2 via
// net/http.Server) so SSE replies (delegate text streamed incrementally)
// keep working exactly as before.
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

// passthroughHTTPS forwards req to the real origin over a real TLS
// connection that offers both h2 and http/1.1 via ALPN, and writes the
// origin's response back through w. Uses http.Transport instead of a
// hand-rolled tls.Client+req.Write+http.ReadResponse (the pre-2026-07-28
// version): net/http's own transport already handles ALPN negotiation
// with the real origin and response framing for both protocol versions —
// the hand-rolled version assumed HTTP/1.1 unconditionally and broke
// against any genuinely HTTP/2-only origin (this function's sibling fix in
// mitmSession's own doc comment has the live-confirmed example).
func (p *Proxy) passthroughHTTPS(w http.ResponseWriter, req *http.Request, host, hostport string) error {
	// sharedPassthroughTransport, not a fresh Transport per call — see its
	// own doc comment for the full reasoning and the live investigation
	// that led here. ServerName is intentionally not set per-call anymore;
	// the shared Transport derives it correctly per request on its own.
	transport := p.sharedPassthroughTransport()

	// An independent context, NOT req.Context() — the actual root cause of
	// a long-chased ~29-30s hang-then-"context canceled" pattern
	// (2026-07-30, via net/http/httptrace instrumentation pinpointing the
	// block to io.Copy(w, resp.Body), not the dial or the request write,
	// which both complete in well under a second). req.Context() is
	// cursor-agent's OWN inbound connection's context, and cursor-agent's
	// real client sends RST_STREAM(CANCEL) on its own request stream
	// almost immediately after sending it (confirmed live via an isolated
	// wirecapture HTTP/2 frame trace earlier the same investigation) —
	// independent of how fast or slow Glider's own response ends up
	// being. Cloning the outbound request with that same, already-doomed
	// context means cursor-agent's own early self-cancellation poisons
	// Glider's entire relay to the real origin, including the response
	// body copy — even when that relay is otherwise proceeding
	// correctly and would complete fine left alone. A generous, bounded,
	// independent timeout lets the outbound leg live or die on its own
	// merits instead of inheriting a client-side cancellation that may
	// have nothing to do with whether the real origin can actually
	// answer. 120s here is its own independently-reasoned ceiling for a
	// real network response from a real vendor origin (cursor.sh) — not
	// coupled to vendors.RunTimeout (a local subprocess exec bound, now
	// unbounded by default for unrelated reasons; this comment used
	// to claim the two intentionally matched, which stopped being true
	// the moment only one of them changed).
	//
	// AgentService/Run gets a much shorter ceiling — real, live-confirmed
	// incident (2026-07-31): decoupling the outbound relay's lifetime
	// from the inbound client's own (the independent-context fix above)
	// solved the false-positive-cancellation problem, but created a new
	// one — cursor-agent's real client gives up waiting and reconnects on
	// its own ~30s cadence (confirmed live: 4 reconnect attempts, ~30-34s
	// apart), but each abandoned relay here kept running for the FULL
	// 120s regardless, since nothing ever told it the client had moved
	// on. A new diagnostic counter (inFlightAgentRunRelays) proved these
	// pile up: concurrent_in_flight climbed 1→2→3→4 across one real
	// multi-reconnect exchange, meaning by the 4th attempt three already-
	// abandoned relays were still alive, competing for real resources
	// (decrypt workers, TLS negotiation, network) with the attempt that
	// might actually succeed — a real, measurable drag on exactly the
	// connections most likely to matter. There's no reliable way to
	// detect "the client genuinely gave up" directly (req.Context() is
	// exactly what the independent-context fix above correctly avoids,
	// for the reason stated there), so instead of detecting abandonment,
	// this just refuses to hold a relay open longer than the client's own
	// patience window would ever make useful: 40s, a little past the
	// observed ~30-34s reconnect cadence to give a genuinely-just-slow
	// connection a fair chance, but not the 3-4x-longer margin that let
	// abandoned relays pile up before.
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
	// hostport (not host) so the Transport's own dialer resolves/connects
	// to the right address; Host below still carries the bare hostname for
	// the actual HTTP Host header, matching the original's split behavior.
	outReq.URL.Host = hostport
	outReq.Host = host

	// GetBody, not just Body — real, live-confirmed fallout from switching
	// to a shared, pooled Transport (2026-07-29): a pooled connection can
	// go stale between uses (the origin closed it, a NAT/firewall dropped
	// it, etc.), and Go's Transport automatically retries such a failure
	// on a fresh connection *if* it can rewind the request body — which it
	// can't for a plain io.ReadCloser with no GetBody set, surfacing as
	// "net/http: cannot rewind body after connection loss" instead of a
	// transparent retry.
	//
	// Only for a body with a KNOWN length (req.ContentLength >= 0) — a
	// body with an unknown length (cursor-agent's AgentService/Run,
	// reconstructed upstream as a live stream: buffered first envelope +
	// the real client's still-arriving bytes, see DelegateHandler and
	// handleAgentRPC's own doc comments) must NOT be read eagerly here.
	// Doing so would block this whole call on the client's own pace
	// again — exactly the blocking-read problem this investigation
	// already fixed once, just relocated here. For that case, Body is
	// relayed live and GetBody is left unset; a stale pooled connection
	// on a bidi-streaming relay is a rare-enough edge case not worth
	// paying for with a reintroduced block on every request.
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

	// flushAfterWrite, not a bare io.Copy(w, ...) — THE root cause of the
	// long-chased cursor-agent hang, found 2026-07-31 with exact numeric
	// confirmation. net/http buffers response writes internally
	// (bufferBeforeChunkingSize = 2048 for HTTP/1.1;
	// http2handlerChunkWriteSize = 4096 for HTTP/2) and only lets them
	// out when that buffer fills, the handler returns, or someone calls
	// Flush. This relay called Flush nowhere, so a streaming RPC response
	// — which is exactly a slow trickle of small frames, by design —
	// accumulated in the buffer instead of reaching the client.
	//
	// The live data was bimodal and unmistakable: every stuck
	// AgentService/Run stream plateaued at 116-215 bytes (safely under
	// both thresholds — invisible to the client, which then hit its own
	// ~30s patience window and reconnected, repeatedly, for 160-420s),
	// while every stream that actually delivered had crossed 14KB
	// (blowing past 4096 and auto-flushing as a side effect, which is the
	// only reason this ever appeared to "work eventually" at all). This
	// also explains why killing glider.exe made the same in-flight prompt
	// resolve instantly — no buffering proxy left in the path — and why
	// small delegate replies were hit hardest: they're the smallest
	// responses of all. Note the ngl origin adapters (adapter_*_origin.go)
	// have always flushed explicitly for exactly this reason; plain
	// passthrough was simply missing the equivalent.
	//
	// Wrapping w (rather than passing it directly) additionally prevents
	// io.Copy from taking net/http's io.ReaderFrom fast path, which would
	// bypass per-write flushing entirely.
	var dst io.Writer = w
	if flusher, ok := w.(http.Flusher); ok {
		dst = flushAfterWrite{w: w, flusher: flusher}
	}
	_, copyErr := io.Copy(dst, resp.Body)
	return copyErr
}

// flushAfterWrite pushes every write straight through to the client
// instead of letting it sit in net/http's internal response buffer — see
// its use in passthroughHTTPS for the full incident writeup. Permanent,
// not diagnostic: any relay of a streaming response (Connect/gRPC bidi,
// SSE, chunked long-poll) is broken without it whenever the response's
// frames are individually smaller than the buffer, which for a
// heartbeat-driven protocol is the normal case rather than an edge case.
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

// singleConnListener adapts one already-accepted net.Conn to the
// net.Listener interface http.Server.ServeTLS requires, so ServeTLS's own
// ALPN negotiation and request dispatch apply to exactly this one MITM'd
// connection.
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
