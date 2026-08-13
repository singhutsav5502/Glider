// Command wirecapture is a research tool that operates alone. It is a small
// CONNECT proxy. It decrypts the traffic of one CLI and writes the raw shape
// of the requests and the responses. It is fully separate from the process of
// glider.exe and from the shared code that handles requests.
//
// Point a vendor CLI at it with HTTP_PROXY and HTTPS_PROXY. Live tests
// confirmed that cursor-agent and agy both use these variables for their true
// completion-plane traffic.
//
// Use this tool, and do not put instruments in internal/mitm/proxy.go. That
// method risks damage to each true request that goes through the shared code
// at the same time, and this includes the session of the operator of Glider.
// This research pass confirmed that risk in a difficult way.
//
// This started as one temporary pass. A person kept it as a true tool for
// development on 2026-07-28, after it gave the information for a correction in
// the product. The true capture of HTTP/2 that this tool made possible (refer
// to -hosts below) proved that passthroughHTTPS in internal/mitm/proxy.go
// needed true support for ALPN and h2, and not only http/1.1. A person
// corrected that on the same day. This tool is probably of use again for the
// next question about a wire format, or for a new version of a vendor CLI.
//
// The tool uses the CA of Glider, with internal/mitm.LoadOrCreateAuthority.
// Therefore a person needs no other step for trust, other than the step that a
// test of delegation already needs.
//
// The tool sends the traffic to the true origin with the Transport of
// net/http, which agrees HTTP/2 automatically with ALPN when the origin
// accepts it. The TLS that faces the client, on a decrypted connection, also
// offers true ALPN. It uses net/http.Server.ServeTLS on a listener with one
// connection. Refer to handleConn.
//
// The earlier version of this tool did not offer ALPN. Therefore it stopped
// permanently against each host that always agrees h2, and it gave no message.
// The true completion-plane host of cursor-agent does exactly that. That stop
// was the signal that this tool and proxy.go both needed a correction.
//
// The captured content under -dumpdir holds true bearer tokens and other data
// of a session. Never put that content in the repository. The tool writes it
// outside the repository by default, in ~/.glider/wirecapture, and git does
// not track *.exe or the output. But use that as the last protection, and not
// as the plan. Delete the directory with the dumps when a research pass
// completes.
//
// Usage:
//
//	go run ./tools/wirecapture -port 18082 -dumpdir C:\Users\me\.glider\wirecapture -hosts host1,host2
//	$env:HTTP_PROXY = "http://127.0.0.1:18082"
//	$env:HTTPS_PROXY = "http://127.0.0.1:18082"
//	# run the vendor CLI headless one time, then stop this tool with Ctrl+C
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

	// Server.ServeTLS of net/http agrees h2 or http/1.1 automatically, with ALPN,
	// and it sends both to the same http.Handler.
	//
	// This replaces an earlier version that used tls.Server() and a manual branch
	// for ALPN, and whose h2 branch did nothing. That was a true finding: the
	// completion-plane host of cursor-agent, agentn.global.api5.cursor.sh, always
	// agrees h2. The earlier version stopped permanently against that host and
	// gave no message, and the client tried again with an interval that increased,
	// against a connection that could never answer.
	//
	// The H2 server of the standard library removes the need to write the code for
	// HPACK and for the frames, in a research tool that a person can discard.
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

// captureHandler writes one pair of a request and a response, and it sends the
// request to the true origin.
//
// It operates with no change for h1.1 and for h2, because http.Server already
// made both into the same http.Handler contract.
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
		// Do not call WriteHeader or Flush before the body.
		//
		// An explicit flush at the start sends the HEADERS frame as its own separate
		// write on the network, before any byte of the body is ready. That is a second
		// point where the code flushes: first the headers, then the body. That is
		// exactly the pattern that a person examines here.
		//
		// Let the one call to w.Write(body) below cause an implicit WriteHeader(200).
		// Then the buffer of Go can join the headers and the body into as few writes as
		// it can.
		w.WriteHeader(resp.StatusCode)
		// An experiment on 2026-07-29.
		//
		// The streaming path below uses io.Copy and a flush after each part. It failed
		// 4 times of 4, and always at exactly 9 bytes. That is the size of the first
		// AgentServerMessage envelope.
		//
		// That result suggests one condition: the client of cursor-agent accepts exactly
		// one write and one flush on a connection that a MITM decrypts to this host, and
		// it resets the connection at the second one.
		//
		// This mode tests that condition directly. It collects each Read(). An earlier
		// run of this same experiment confirmed that the origin gives the two envelopes
		// as two separate reads.
		//
		// It collects them until one of two events. The first event: singleWriteWindow
		// passes with *no new data*. That is not a fixed limit from the start. A fixed
		// limit either operates before the origin answers, which a first attempt at this
		// showed, or it is slow with no cause. The second event: a true EOF arrives. The
		// body of a response from an RPC in two directions can never reach EOF.
		//
		// Then the code makes exactly one Write, with no flush between.
		type readResult struct {
			chunk []byte
			err   error
		}
		// This channel has a buffer. Therefore the goroutine that reads can still give
		// its last result, which can be an error, and then exit cleanly. That is true
		// also when this handler already stopped listening, because its idle time limit
		// operated first.
		//
		// This is a diagnostic tool that a person can discard. A full path for
		// cancellation is not necessary.
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

	// Send the bytes of the response to the client as they arrive, and flush after
	// each part. This is the same design as passthroughHTTPS of Glider: io.Copy, and
	// not a full buffer before one write.
	//
	// The first version of this handler called io.ReadAll(resp.Body) before it wrote
	// anything back.
	//
	// This tool exists to examine agent.v1.AgentService/Run, which agent_v1.proto
	// shows is a true RPC that streams in two directions. For such an RPC, that
	// first version made the client wait for the *full* turn before it saw one byte.
	//
	// That condition is a second cause that can hide the true cause. This new
	// version removes it. Therefore a person does not mix "the client stopped" with
	// "wirecapture kept the data in a buffer".
	var captured bytes.Buffer
	tee := io.TeeReader(resp.Body, &captured)
	n, copyErr := io.Copy(&flushWriter{w: w, flusher: flusher}, tee)
	dumpResponse(h.dumpDir, h.host, r.URL.Path, resp.StatusCode, resp.Header, captured.Bytes())
	if copyErr != nil {
		log.Printf("wirecapture: streaming response for %s%s failed after %d bytes: %v", h.host, r.URL.Path, n, copyErr)
	}
}

// flushWriter flushes after each Write. Therefore the bytes of a streaming
// response arrive at the client as they come. They do not stay in the internal
// buffer of Go until sufficient data collects.
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

// singleConnListener changes one net.Conn that the code accepted before into
// the net.Listener interface. That connection is raw, before TLS, from the
// CONNECT handling of this tool.
//
// http.Server.ServeTLS needs that interface. Therefore the automatic ALPN
// negotiation of ServeTLS, between h2 and h1.1, applies to exactly this one
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

// shouldDecrypt says if host agrees with one of the suffixes in -hosts.
//
// If no person set -hosts, the tool decrypts each connection. That is the first
// behaviour of this tool, for a capture with a short and exact target.
//
// After a person gives a list of hosts, each host that is not on that list goes
// through a blind tunnel, and this code does not decrypt it. An example is
// lh3.googleusercontent.com, which gets a picture for a profile.
//
// Therefore the behaviour of a host with no relation to this work cannot make
// the vendor CLI believe that the full session failed. An example of such a
// behaviour is a host that agrees h2 against serveH1, which speaks http/1.1
// only.
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

// blindTunnel sends raw bytes between the client and the true host on port 443,
// and it ends no TLS connection.
//
// This is the same shape as the passthrough path of Glider in
// internal/mitm/proxy.go. But it has no question about ALPN.
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
