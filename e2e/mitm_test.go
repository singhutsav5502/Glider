package e2e_test

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/mitm"
)

// E2E: MITM CONNECT → decrypt → passthrough to TLS upstream.
func TestMITME2EPassthrough(t *testing.T) {
	upAuth, err := mitm.GenerateAuthority()
	if err != nil {
		t.Fatal(err)
	}
	upLeaf, err := upAuth.CertificateForHost("127.0.0.1")
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
		_, _ = io.WriteString(w, "e2e-upstream")
	}))

	ca, err := mitm.GenerateAuthority()
	if err != nil {
		t.Fatal(err)
	}
	p := &mitm.Proxy{
		Addr:            "127.0.0.1:0",
		Authority:       ca,
		Hosts:           mitm.NewHostMatcher([]string{"127.0.0.1"}),
		PassthroughOnly: true,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	defer p.Shutdown(t.Context())

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.CertPEM())
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: p.ListenAddr()}),
			TLSClientConfig: &tls.Config{
				RootCAs:    pool,
				ServerName: "127.0.0.1",
			},
		},
	}
	resp, err := client.Get("https://" + upLn.Addr().String() + "/hello")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "e2e-upstream" {
		t.Fatalf("got %q", b)
	}
}

// E2E: MITM local stub on chat completions path.
func TestMITME2ELocal(t *testing.T) {
	ca, err := mitm.GenerateAuthority()
	if err != nil {
		t.Fatal(err)
	}
	local := &e2eLocal{}
	p := &mitm.Proxy{
		Addr:      "127.0.0.1:0",
		Authority: ca,
		Hosts:     mitm.NewHostMatcher([]string{"127.0.0.1"}),
		Local:     local,
	}
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	defer p.Shutdown(t.Context())

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.CertPEM())
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: p.ListenAddr()}),
			TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "127.0.0.1"},
		},
	}
	// Any 127.0.0.1 CONNECT target; local handler short-circuits before upstream dial.
	resp, err := client.Post("https://127.0.0.1:9/v1/chat/completions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if string(b) != `{"local":true}` {
		t.Fatalf("body=%q", b)
	}
}

type e2eLocal struct{}

func (e *e2eLocal) TryHandle(w http.ResponseWriter, r *http.Request) (bool, error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"local":true}`)
	return true, nil
}
