package mitm_test

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
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
