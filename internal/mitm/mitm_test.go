package mitm_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/mitm"
)

func TestHostMatcher(t *testing.T) {
	m := mitm.NewHostMatcher([]string{
		"api2.cursor.sh",
		"api3.cursor.sh",
		"api4.cursor.sh",
		"*.api5.cursor.sh",
	})
	cases := []struct {
		host string
		want bool
	}{
		{"api2.cursor.sh", true},
		{"api2.cursor.sh:443", true},
		{"api3.cursor.sh", true},
		{"api4.cursor.sh", true},
		{"foo.api5.cursor.sh", true},
		{"api5.cursor.sh", true}, // apex of *.api5.cursor.sh
		{"cursor.sh", false},
		{"www.cursor.sh", false},
		{"api.openai.com", false},
		{"evil.com", false},
	}
	for _, tc := range cases {
		if got := m.Match(tc.host); got != tc.want {
			t.Fatalf("Match(%q)=%v want %v", tc.host, got, tc.want)
		}
	}
}

func TestAuthorityMintAndPersist(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "ca.crt")
	keyPath := filepath.Join(dir, "ca.key")
	a, err := mitm.LoadOrCreateAuthority(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.CertPEM()) == 0 {
		t.Fatal("empty CA PEM")
	}
	leaf, err := a.CertificateForHost("api2.cursor.sh")
	if err != nil {
		t.Fatal(err)
	}
	if leaf.Leaf.Subject.CommonName != "api2.cursor.sh" {
		t.Fatalf("CN=%s", leaf.Leaf.Subject.CommonName)
	}
	// Reload
	a2, err := mitm.LoadOrCreateAuthority(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(a2.CertPEM()) != string(a.CertPEM()) {
		t.Fatal("reloaded CA mismatch")
	}
}

func TestMITMPassthroughCONNECT(t *testing.T) {
	// Upstream TLS server with self-signed cert for 127.0.0.1
	upLeafAuth, err := mitm.GenerateAuthority()
	if err != nil {
		t.Fatal(err)
	}
	upLeaf, err := upLeafAuth.CertificateForHost("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	upTLS := &tls.Config{Certificates: []tls.Certificate{*upLeaf}}
	upLn, err := tls.Listen("tcp", "127.0.0.1:0", upTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer upLn.Close()
	go http.Serve(upLn, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", "yes")
		_, _ = io.WriteString(w, "upstream-ok")
	}))

	auth, err := mitm.GenerateAuthority()
	if err != nil {
		t.Fatal(err)
	}
	proxy := &mitm.Proxy{
		Addr:      "127.0.0.1:0",
		Authority: auth,
		Hosts:     mitm.NewHostMatcher([]string{"127.0.0.1"}),
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS12,
		},
		PassthroughOnly: true,
	}
	if err := proxy.Start(); err != nil {
		t.Fatal(err)
	}
	defer proxy.Shutdown(t.Context())

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(auth.CertPEM())
	transport := &http.Transport{
		Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: proxy.ListenAddr()}),
		TLSClientConfig: &tls.Config{
			RootCAs:    pool,
			ServerName: "127.0.0.1",
			MinVersion: tls.VersionTLS12,
		},
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}

	upHost := upLn.Addr().String()
	resp, err := client.Get("https://" + upHost + "/v1/ping")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.Header.Get("X-Upstream") != "yes" {
		t.Fatalf("headers=%v", resp.Header)
	}
	if string(body) != "upstream-ok" {
		t.Fatalf("body=%q", body)
	}
}

func TestMITMLocalIntercept(t *testing.T) {
	auth, err := mitm.GenerateAuthority()
	if err != nil {
		t.Fatal(err)
	}
	local := &stubLocal{body: `{"ok":true,"source":"local"}`}
	proxy := &mitm.Proxy{
		Addr:      "127.0.0.1:0",
		Authority: auth,
		Hosts:     mitm.NewHostMatcher([]string{"127.0.0.1"}),
		Local:     local,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
	}
	if err := proxy.Start(); err != nil {
		t.Fatal(err)
	}
	defer proxy.Shutdown(t.Context())

	// Dummy upstream — should not be hit when local handles.
	upLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upLn.Close()

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(auth.CertPEM())
	transport := &http.Transport{
		Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: proxy.ListenAddr()}),
		TLSClientConfig: &tls.Config{
			RootCAs:    pool,
			ServerName: "127.0.0.1",
		},
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	req, _ := http.NewRequest(http.MethodPost, "https://"+upLn.Addr().String()+"/v1/chat/completions", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != local.body {
		t.Fatalf("body=%q", body)
	}
	if !local.called {
		t.Fatal("local handler not called")
	}
}

type stubLocal struct {
	called bool
	body   string
}

func (s *stubLocal) TryHandle(w http.ResponseWriter, r *http.Request) (bool, error) {
	s.called = true
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, s.body)
	return true, nil
}

func TestBlindTunnelNonAllowlisted(t *testing.T) {
	// Plain TCP echo upstream
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 64)
		n, _ := c.Read(buf)
		_, _ = c.Write([]byte("echo:" + string(buf[:n])))
	}()

	proxy := &mitm.Proxy{
		Addr:  "127.0.0.1:0",
		Hosts: mitm.NewHostMatcher([]string{"never.match"}),
	}
	if err := proxy.Start(); err != nil {
		t.Fatal(err)
	}
	defer proxy.Shutdown(t.Context())

	conn, err := net.DialTimeout("tcp", proxy.ListenAddr(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", ln.Addr().String(), ln.Addr().String())
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !containsHTTP200(string(buf[:n])) {
		t.Fatalf("connect resp=%q", buf[:n])
	}
	_, _ = conn.Write([]byte("hello"))
	_, _ = conn.Read(buf)
}

func containsHTTP200(s string) bool {
	return stringContains(s, "200 Connection") || stringContains(s, "200 OK")
}

func stringContains(s, sub string) bool {
	return strings.Contains(s, sub)
}

// failingRedirector always fails Start — stands in for WinDivertOpen
// failing on a non-elevated process, the real, live-reported cause this
// test guards against.
type failingRedirector struct{}

func (failingRedirector) Start(ctx context.Context, cfg mitm.RedirectConfig) error {
	return fmt.Errorf("simulated: WinDivertOpen requires Administrator privileges")
}
func (failingRedirector) Stop() error { return nil }

// TestProxyStart_TransparentRedirectorFailureIsNonFatal is the direct
// regression test for a real, live-reported bug (2026-07-28): a
// double-clicked, non-elevated glider.exe vanished instantly with no tray
// icon and no visible error. Root cause: mitm.transparent defaults to true
// (2026-07-26), and WinDivertOpen requires Administrator privileges to
// load its kernel driver — a failure that used to make Proxy.Start return
// an error, which main.go turned into os.Exit(1), killing a process that
// had already successfully started its gateway and dashboard over a
// failure in one optional enhancement. Proxy.Start must now succeed (and
// the CONNECT-based proxy must still actually work) even when the
// transparent redirector can't start at all.
func TestProxyStart_TransparentRedirectorFailureIsNonFatal(t *testing.T) {
	upLeafAuth, err := mitm.GenerateAuthority()
	if err != nil {
		t.Fatal(err)
	}
	upLeaf, err := upLeafAuth.CertificateForHost("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	upLn, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{*upLeaf}})
	if err != nil {
		t.Fatal(err)
	}
	defer upLn.Close()
	go http.Serve(upLn, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "upstream-ok")
	}))

	auth, err := mitm.GenerateAuthority()
	if err != nil {
		t.Fatal(err)
	}
	proxy := &mitm.Proxy{
		Addr:            "127.0.0.1:0",
		Authority:       auth,
		Hosts:           mitm.NewHostMatcher([]string{"127.0.0.1"}),
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12},
		PassthroughOnly: true,
		Redirector:      failingRedirector{},
		TransparentPort: 0, // set below once we know a free port isn't needed — see note
	}
	// A nonzero TransparentPort is required to even attempt starting the
	// redirector (see Proxy.Start's own guard) — any free port works
	// since failingRedirector never actually uses it.
	proxy.TransparentPort = 39001

	if err := proxy.Start(); err != nil {
		t.Fatalf("expected Start() to succeed despite a failing transparent redirector, got: %v", err)
	}
	defer proxy.Shutdown(t.Context())

	// The CONNECT-based proxy must still genuinely work.
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(auth.CertPEM())
	transport := &http.Transport{
		Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: proxy.ListenAddr()}),
		TLSClientConfig: &tls.Config{
			RootCAs:    pool,
			ServerName: "127.0.0.1",
			MinVersion: tls.VersionTLS12,
		},
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	resp, err := client.Get("https://" + upLn.Addr().String() + "/v1/ping")
	if err != nil {
		t.Fatalf("CONNECT-based proxy should still work: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "upstream-ok" {
		t.Fatalf("body=%q", body)
	}
}

// TestMITMPassthroughHTTP2 is the direct regression test for the real,
// live-confirmed bug fixed 2026-07-28: mitmSession/passthroughHTTPS used to
// offer no ALPN at all to the client side and assume HTTP/1.1 framing
// unconditionally against the real origin — broken against any genuinely
// HTTP/2-only peer (confirmed live: cursor-agent's actual completion-plane
// host, agentn.global.api5.cursor.sh, negotiates h2 unconditionally — see
// planning/agent_cli_interop.md's "cursor-agent exact completion-plane RPC
// name" entry). Uses a real HTTP/2 origin (httptest.Server.EnableHTTP2) and
// a real HTTP/2-capable client Transport, both going through Glider's own
// CONNECT proxy — proves the whole path negotiates and speaks genuine h2,
// not just a same-shape HTTP/1.1 response that happens to pass a header check.
func TestMITMPassthroughHTTP2(t *testing.T) {
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Proto", r.Proto)
		_, _ = io.WriteString(w, "h2-upstream-ok")
	}))
	upstream.EnableHTTP2 = true
	upstream.StartTLS()
	defer upstream.Close()

	upURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	auth, err := mitm.GenerateAuthority()
	if err != nil {
		t.Fatal(err)
	}
	proxy := &mitm.Proxy{
		Addr:      "127.0.0.1:0",
		Authority: auth,
		Hosts:     mitm.NewHostMatcher([]string{upURL.Hostname()}),
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS12,
		},
		PassthroughOnly: true,
	}
	if err := proxy.Start(); err != nil {
		t.Fatal(err)
	}
	defer proxy.Shutdown(t.Context())

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(auth.CertPEM())
	transport := &http.Transport{
		Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: proxy.ListenAddr()}),
		TLSClientConfig: &tls.Config{
			RootCAs:    pool,
			ServerName: upURL.Hostname(),
			MinVersion: tls.VersionTLS12,
		},
		ForceAttemptHTTP2: true,
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}

	resp, err := client.Get(upstream.URL + "/v1/ping")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.ProtoMajor != 2 {
		t.Fatalf("got Proto=%q, want HTTP/2 end-to-end through the MITM proxy", resp.Proto)
	}
	if resp.Header.Get("X-Proto") != "HTTP/2.0" {
		t.Fatalf("origin itself saw Proto=%q, want HTTP/2.0 — the MITM hop must not have downgraded the request", resp.Header.Get("X-Proto"))
	}
	if string(body) != "h2-upstream-ok" {
		t.Fatalf("body=%q", body)
	}
}
