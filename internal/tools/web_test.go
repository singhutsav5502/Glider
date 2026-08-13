package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestWebSearchCatalogAndParse(t *testing.T) {
	r := NewRegistry(Options{})
	names := map[string]bool{}
	for _, s := range r.Catalog(context.Background()) {
		names[s.Name] = true
	}
	for _, n := range []string{"web_search", "web_fetch", "http_fetch"} {
		if !names[n] {
			t.Fatalf("missing %s", n)
		}
	}
	calls := ParseTextToolCalls("```json\n" + `{"name":"web_search","arguments":{"query":"CVE-2024-1234 openssl","limit":3}}` + "\n```")
	if len(calls) != 1 || calls[0].Name != "web_search" {
		t.Fatalf("parse: %+v", calls)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(calls[0].Arguments), &args); err != nil {
		t.Fatal(err)
	}
	if args["query"] != "CVE-2024-1234 openssl" {
		t.Fatalf("args=%v", args)
	}
}

func TestWebSearchBraveMock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Subscription-Token") == "" {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"web": map[string]any{
				"results": []map[string]string{
					{"title": "Hit A", "url": "https://example.com/a", "description": "snippet a"},
					{"title": "Hit B", "url": "https://example.com/b", "description": "snippet b"},
				},
			},
		})
	}))
	defer srv.Close()

	t.Setenv("BRAVE_SEARCH_API_KEY", "test-key")
	t.Setenv("BRAVE_API_KEY", "")
	t.Setenv("TAVILY_API_KEY", "")
	t.Setenv("SERPAPI_KEY", "")
	_ = os.Unsetenv("SEARXNG_URL")

	tool := &webSearchTool{
		cfg: WebSearchOptions{Provider: "brave", MaxResults: 5},
		httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			req2 := req.Clone(req.Context())
			req2.URL.Scheme = "http"
			req2.URL.Host = strings.TrimPrefix(srv.URL, "http://")
			req2.URL.Path = "/"
			req2.URL.RawQuery = ""
			req2.RequestURI = ""
			return http.DefaultTransport.RoundTrip(req2)
		})},
	}
	res, err := tool.Call(context.Background(), "", json.RawMessage(`{"query":"openssl advisory","limit":2}`))
	if err != nil || !res.OK {
		t.Fatalf("%+v err=%v", res, err)
	}
	if !strings.Contains(res.Output, "provider=brave") || !strings.Contains(res.Output, "Hit A") {
		t.Fatalf("output=%s", res.Output)
	}
}

func TestWebSearchMissingKeyedProvider(t *testing.T) {
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	t.Setenv("BRAVE_API_KEY", "")
	t.Setenv("TAVILY_API_KEY", "")
	t.Setenv("SERPAPI_KEY", "")
	tool := &webSearchTool{cfg: WebSearchOptions{Provider: "tavily"}}
	res, err := tool.Call(context.Background(), "test query", nil)
	if err == nil || res.OK {
		t.Fatalf("expected error, got %+v", res)
	}
	if !strings.Contains(res.Err, "TAVILY_API_KEY") {
		t.Fatalf("err=%s", res.Err)
	}
}

func TestWebFetchAllowlistAndStrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><script>x()</script><p>Hello advisory</p></body></html>`))
	}))
	defer srv.Close()

	u, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	host := u.URL.Hostname()
	r := NewRegistry(Options{AllowHosts: []string{host}, WebSearch: WebSearchOptions{FetchMaxBytes: 4096}})
	res, err := r.Invoke(context.Background(), Ref{Name: "web_fetch", Kind: KindBuiltin}, srv.URL)
	if err != nil || !res.OK {
		t.Fatalf("%+v err=%v", res, err)
	}
	if !strings.Contains(res.Output, "Hello advisory") {
		t.Fatalf("output=%s", res.Output)
	}
	if strings.Contains(res.Output, "<script>") {
		t.Fatalf("html not stripped: %s", res.Output)
	}

	res, err = r.Invoke(context.Background(), Ref{Name: "web_fetch", Kind: KindBuiltin}, "https://evil.example/x")
	if err == nil && res.OK {
		t.Fatalf("expected allowlist deny")
	}
}

func TestHttpFetchAllowlist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("raw-body"))
	}))
	defer srv.Close()
	u, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	host := u.URL.Hostname()
	r := NewRegistry(Options{AllowHosts: []string{host}})
	res, err := r.Invoke(context.Background(), Ref{Name: "http_fetch", Kind: KindBuiltin}, srv.URL)
	if err != nil || !res.OK || !strings.Contains(res.Output, "raw-body") {
		t.Fatalf("%+v err=%v", res, err)
	}
}

func TestParseDuckDuckGoHTML(t *testing.T) {
	page := `
<a class="result__a" href="https://duckduckgo.com/l/?uddg=https%3A%2F%2Fnvd.nist.gov%2Fvuln">NVD CVE</a>
<a class="result__snippet">OpenSSL advisory details</a>
`
	hits := parseDuckDuckGoHTML(page, 5)
	if len(hits) != 1 || !strings.Contains(hits[0].URL, "nvd.nist.gov") {
		t.Fatalf("%+v", hits)
	}
}

func TestStandardNamesIncludesWeb(t *testing.T) {
	joined := strings.Join(StandardNames(), ",")
	if !strings.Contains(joined, "web_search") || !strings.Contains(joined, "web_fetch") {
		t.Fatalf("StandardNames=%v", StandardNames())
	}
}

func TestWebFetchSizeCap(t *testing.T) {
	big := strings.Repeat("abcdefghij", 2000) // 20KiB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(big))
	}))
	defer srv.Close()
	u, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	host := u.URL.Hostname()
	r := NewRegistry(Options{AllowHosts: []string{host}, WebSearch: WebSearchOptions{FetchMaxBytes: 1024}})
	res, err := r.Invoke(context.Background(), Ref{Name: "web_fetch"}, srv.URL)
	if err != nil || !res.OK {
		t.Fatalf("%+v err=%v", res, err)
	}
	if !strings.Contains(res.Output, "truncated=true") {
		t.Fatalf("expected size cap truncate: %s", res.Output[:minInt(180, len(res.Output))])
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
