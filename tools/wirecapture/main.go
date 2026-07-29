// Command wirecapture is a standalone research tool — a minimal CONNECT
// proxy that decrypts one CLI's traffic to dump its raw request/response
// shape, entirely separate from glider.exe's own process and shared
// request-handling code. Point a vendor CLI at it via
// HTTP_PROXY/HTTPS_PROXY (both cursor-agent and agy have been confirmed
// live to honor these for their real completion-plane traffic) instead of
// injecting instrumentation into internal/mitm/proxy.go, which risks
// corrupting whatever real traffic (including Glider's own operator
// session) happens to be flowing through the shared code at the same
// time — confirmed the hard way earlier in this research pass.
//
// Started as a one-off, throwaway pass; kept as a real dev tool
// (2026-07-28) once it directly informed a production fix — the real
// HTTP/2 capture this tool made possible (see -hosts below) is what
// proved internal/mitm/proxy.go's own passthroughHTTPS needed genuine
// ALPN/h2 support, not just http/1.1 (fixed the same day). Likely useful
// again for the next wire-format question or vendor CLI update.
//
// Reuses Glider's own CA (internal/mitm.LoadOrCreateAuthority) so no
// separate trust step is needed beyond what delegation testing already
// requires. Forwards to real origin via net/http's Transport, which
// negotiates HTTP/2 automatically via ALPN when the origin supports it.
// A decrypted connection's OWN client-facing TLS also offers real ALPN
// (net/http.Server.ServeTLS on a single-connection listener, see
// handleConn) — the earlier version of this tool didn't, and silently
// hung forever against any host that negotiates h2 unconditionally
// (cursor-agent's real completion-plane host does exactly that; that hang
// was itself the clue this needed fixing, both here and in proxy.go).
//
// Captured dumps under -dumpdir contain real bearer tokens and other
// session data — never commit them (they're written outside the repo by
// default, ~/.glider/wirecapture, and *.exe/dump output isn't tracked
// either way, but treat that as a backstop, not the plan). Delete the
// dump directory when a research pass concludes.
//
// Usage:
//
//	go run ./tools/wirecapture -port 18082 -dumpdir C:\Users\me\.glider\wirecapture -hosts host1,host2
//	$env:HTTP_PROXY = "http://127.0.0.1:18082"
//	$env:HTTPS_PROXY = "http://127.0.0.1:18082"
//	# run the vendor CLI headless once, then Ctrl+C this tool
package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glider-ai/glider/internal/mitm"
)

var decryptHosts []string

// singleWrite gates the ServeHTTP experiment mode added 2026-07-29 — see
// its own doc comment at the call site. singleWriteWindow bounds how long
// it accumulates reads before issuing the one combined Write.
var (
	singleWrite       bool
	singleWriteWindow time.Duration
)

func main() {
	port := flag.Int("port", 18082, "listen port for the CONNECT proxy")
	dumpDir := flag.String("dumpdir", filepath.Join(os.Getenv("USERPROFILE"), ".glider", "wirecapture"), "directory to write raw request/response dumps to")
	hosts := flag.String("hosts", "", "comma-separated host suffixes to decrypt (e.g. api.anthropic.com,cursor.sh) — everything else is a blind tunnel, same as Glider's own default-passthrough behavior. Empty means decrypt everything (only safe for a short, targeted capture).")
	flag.BoolVar(&singleWrite, "singlewrite", false, "experiment: relay the response as one Write with no intermediate flush, instead of streaming — see ServeHTTP's doc comment")
	flag.DurationVar(&singleWriteWindow, "singlewrite-window", 500*time.Millisecond, "how long -singlewrite accumulates reads before issuing the one combined Write")
	flag.Parse()
	if *hosts != "" {
		decryptHosts = strings.Split(*hosts, ",")
	}

	if err := os.MkdirAll(*dumpDir, 0o755); err != nil {
		log.Fatalf("wirecapture: creating dump dir: %v", err)
	}

	certPath, keyPath := mitm.DefaultCAPaths()
	auth, err := mitm.LoadOrCreateAuthority(mitm.ExpandPath(certPath), mitm.ExpandPath(keyPath))
	if err != nil {
		log.Fatalf("wirecapture: loading Glider CA: %v", err)
	}

	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("wirecapture: listen %s: %v", addr, err)
	}
	log.Printf("wirecapture: listening on %s, dumping to %s", addr, *dumpDir)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("wirecapture: accept error: %v", err)
			continue
		}
		go handleConn(conn, auth, *dumpDir)
	}
}

func handleConn(client net.Conn, auth *mitm.Authority, dumpDir string) {
	defer client.Close()
	br := bufio.NewReader(client)

	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	if req.Method != http.MethodConnect {
		return
	}
	host := req.URL.Hostname()
	if host == "" {
		host = strings.Split(req.Host, ":")[0]
	}

	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}

	if !shouldDecrypt(host) {
		blindTunnel(client, host)
		return
	}

	leaf, err := auth.CertificateForHost(host)
	if err != nil {
		log.Printf("wirecapture: cert for %s: %v", host, err)
		return
	}

	// net/http's own Server.ServeTLS auto-negotiates h2 vs http/1.1 via
	// ALPN and dispatches either to the same http.Handler — this replaces
	// an earlier hand-rolled tls.Server()+manual-ALPN-branch version whose
	// h2 arm was an unimplemented no-op (real finding: cursor-agent's
	// completion-plane host, agentn.global.api5.cursor.sh, negotiates h2
	// unconditionally and silently hung forever against that stub — the
	// client kept retrying with exponential backoff against a connection
	// that would never respond). Reusing the standard library's own H2
	// server avoids hand-writing HPACK/frame handling for a throwaway
	// research tool.
	ln := newSingleConnListener(client)
	srv := &http.Server{
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{*leaf},
			MinVersion:   tls.VersionTLS12,
		},
		Handler: &captureHandler{host: host, dumpDir: dumpDir},
	}
	if err := srv.ServeTLS(ln, "", ""); err != nil && err != http.ErrServerClosed {
		log.Printf("wirecapture: serve %s: %v", host, err)
	}
}

// captureHandler dumps one request/response pair and forwards to the real
// origin — works unmodified for both h1.1 and h2 since http.Server already
// normalized both into the same http.Handler contract.
type captureHandler struct {
	host    string
	dumpDir string
}

func (h *captureHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	dumpRequest(h.dumpDir, h.host, r.Method, r.URL.Path, r.Proto, r.Header, body)

	outReq, err := http.NewRequest(r.Method, "https://"+h.host+r.URL.RequestURI(), bytes.NewReader(body))
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	outReq.Header = r.Header.Clone()
	outReq.Host = h.host

	transport := &http.Transport{ForceAttemptHTTP2: true}
	resp, err := transport.RoundTrip(outReq)
	if err != nil {
		log.Printf("wirecapture: origin round-trip for %s%s: %v", h.host, r.URL.Path, err)
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	flusher, _ := w.(http.Flusher)

	if singleWrite {
		// No WriteHeader/Flush before the body: an explicit early flush
		// forces the HEADERS frame out as its own separate network write
		// before any body bytes are ready, which is itself a second
		// distinct flush point (headers, then body) — exactly the pattern
		// under suspicion. Letting the single w.Write(body) call below
		// trigger an implicit WriteHeader(200) lets Go's own buffering
		// coalesce headers+body into as few underlying writes as it can.
		w.WriteHeader(resp.StatusCode)
		// Experiment (2026-07-29): the streaming path below (io.Copy,
		// flush after every chunk) failed 4/4 times at exactly 9 bytes —
		// the size of the first AgentServerMessage envelope — suggesting
		// cursor-agent's client tolerates exactly one write/flush over a
		// MITM'd connection to this host and resets on the second. This
		// mode tests that directly: accumulate every Read() (the origin
		// delivers the two envelopes as two separate reads, confirmed by
		// an earlier run of this same experiment) until singleWriteWindow
		// passes with *no new data* (not a fixed deadline from the start —
		// a fixed deadline either fires before the origin even responds,
		// as a first attempt at this found, or is needlessly slow) or a
		// real EOF arrives — a bidi RPC's response body may never reach
		// EOF on its own — then issue exactly one Write, no intermediate
		// flush.
		type readResult struct {
			chunk []byte
			err   error
		}
		// Buffered so the reader goroutine can still deliver its final
		// (possibly error) result and exit cleanly even if this handler
		// has already stopped listening (idle timeout fired first) — a
		// throwaway diagnostic tool, not worth a full cancellation path.
		reads := make(chan readResult, 4)
		go func() {
			buf := make([]byte, 64<<10)
			for {
				n, err := resp.Body.Read(buf)
				chunk := append([]byte(nil), buf[:n]...)
				reads <- readResult{chunk: chunk, err: err}
				if err != nil {
					return
				}
			}
		}()

		var respBody bytes.Buffer
		var readErr error
		idle := time.NewTimer(singleWriteWindow)
		defer idle.Stop()
	accumulate:
		for {
			select {
			case rr := <-reads:
				respBody.Write(rr.chunk)
				if rr.err != nil {
					readErr = rr.err
					break accumulate
				}
				if !idle.Stop() {
					<-idle.C
				}
				idle.Reset(singleWriteWindow)
			case <-idle.C:
				readErr = fmt.Errorf("idle for %s with no new data", singleWriteWindow)
				break accumulate
			}
		}
		dumpResponse(h.dumpDir, h.host, r.URL.Path, resp.StatusCode, resp.Header, respBody.Bytes())
		if _, writeErr := w.Write(respBody.Bytes()); writeErr != nil {
			log.Printf("wirecapture: single-write response for %s%s failed after accumulating %d bytes (readErr=%v): %v", h.host, r.URL.Path, respBody.Len(), readErr, writeErr)
			return
		}
		log.Printf("wirecapture: single-write response for %s%s succeeded, %d bytes (readErr=%v)", h.host, r.URL.Path, respBody.Len(), readErr)
		return
	}

	// Stream response bytes to the client as they arrive, flushing after
	// every chunk — matches Glider's own passthroughHTTPS design
	// (io.Copy, not a full buffer-then-write). The original version of
	// this handler did io.ReadAll(resp.Body) before writing anything back
	// at all, which for a genuinely long-lived streaming RPC (this tool
	// exists specifically to investigate agent.v1.AgentService/Run, a
	// real bidi-streaming RPC per agent_v1.proto) would make the client
	// wait for the *entire* turn to finish before seeing a single byte —
	// a confound this rewrite removes so a client giving up here isn't
	// conflated with wirecapture's own prior buffering limitation.
	var captured bytes.Buffer
	tee := io.TeeReader(resp.Body, &captured)
	n, copyErr := io.Copy(&flushWriter{w: w, flusher: flusher}, tee)
	dumpResponse(h.dumpDir, h.host, r.URL.Path, resp.StatusCode, resp.Header, captured.Bytes())
	if copyErr != nil {
		log.Printf("wirecapture: streaming response for %s%s failed after %d bytes: %v", h.host, r.URL.Path, n, copyErr)
	}
}

// flushWriter flushes after every Write so a streaming response's bytes
// reach the client as they arrive instead of sitting in Go's own internal
// buffering until enough accumulates.
type flushWriter struct {
	w       io.Writer
	flusher http.Flusher
}

func (f *flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if f.flusher != nil {
		f.flusher.Flush()
	}
	return n, err
}

// singleConnListener adapts one already-accepted net.Conn (the raw,
// pre-TLS connection from our own CONNECT handling) to the net.Listener
// interface http.Server.ServeTLS requires, so ServeTLS's own automatic ALPN
// h2/h1.1 negotiation applies to exactly this one connection.
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

func dumpRequest(dumpDir, host, method, path, proto string, header http.Header, body []byte) {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s %s\nHost: %s\n", method, path, proto, host)
	for k, vv := range header {
		for _, v := range vv {
			fmt.Fprintf(&b, "%s: %s\n", k, v)
		}
	}
	fmt.Fprintf(&b, "\n---BODY (%d bytes)---\n%q\n", len(body), body)
	fname := filepath.Join(dumpDir, fmt.Sprintf("REQ_%s_%s_%d.txt", host, sanitize(path), time.Now().UnixNano()))
	_ = os.WriteFile(fname, []byte(b.String()), 0o644)
	log.Printf("wirecapture: dumped request %s%s (%d bytes) -> %s", host, path, len(body), fname)
}

func dumpResponse(dumpDir, host, path string, status int, header http.Header, body []byte) {
	var b strings.Builder
	fmt.Fprintf(&b, "status: %d\n", status)
	for k, vv := range header {
		for _, v := range vv {
			fmt.Fprintf(&b, "%s: %s\n", k, v)
		}
	}
	fmt.Fprintf(&b, "\n---BODY (%d bytes)---\n%q\n", len(body), body)
	fname := filepath.Join(dumpDir, fmt.Sprintf("RESP_%s_%s_%d.txt", host, sanitize(path), time.Now().UnixNano()))
	_ = os.WriteFile(fname, []byte(b.String()), 0o644)
	log.Printf("wirecapture: dumped response %s%s status=%d (%d bytes) -> %s", host, path, status, len(body), fname)
}

// shouldDecrypt reports whether host matches one of the -hosts suffixes.
// If -hosts was never set, everything is decrypted (the tool's original,
// short-targeted-capture behavior). Once a host list is given, anything
// not on it — e.g. lh3.googleusercontent.com fetching a profile picture —
// gets blind-tunneled instead of decrypted, so an unrelated host's TLS
// quirks (like negotiating h2 against our http/1.1-only serveH1) can't
// cascade into the vendor CLI treating the whole session as failed.
func shouldDecrypt(host string) bool {
	if len(decryptHosts) == 0 {
		return true
	}
	for _, suffix := range decryptHosts {
		suffix = strings.TrimSpace(suffix)
		if suffix == "" {
			continue
		}
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

// blindTunnel relays raw bytes between client and the real host on port
// 443 with no TLS termination — same shape as Glider's own passthrough
// path in internal/mitm/proxy.go, just without ALPN concerns since
// nothing here parses the bytes.
func blindTunnel(client net.Conn, host string) {
	origin, err := net.DialTimeout("tcp", net.JoinHostPort(host, "443"), 10*time.Second)
	if err != nil {
		log.Printf("wirecapture: blind tunnel dial %s: %v", host, err)
		return
	}
	defer origin.Close()

	done := make(chan struct{}, 2)
	go func() {
		io.Copy(origin, client)
		done <- struct{}{}
	}()
	go func() {
		io.Copy(client, origin)
		done <- struct{}{}
	}()
	<-done
}

func sanitize(path string) string {
	r := strings.NewReplacer("/", "_", ":", "_", "\\", "_", "?", "_", "*", "_")
	s := r.Replace(path)
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}
