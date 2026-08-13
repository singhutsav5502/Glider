package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultWebSearchLimit = 5
	maxWebSearchLimit     = 10
	defaultWebFetchBytes  = 64 << 10
	maxWebFetchBytes      = 256 << 10
)

// WebSearchOptions configures web_search / web_fetch builtins.
type WebSearchOptions struct {
	// Provider is auto|duckduckgo|brave|tavily|serpapi|searxng (default auto).
	Provider string
	// MaxResults caps ranked hits (default 5, max 10).
	MaxResults int
	// BraveAPIKeyEnv names the env var for Brave Search (default BRAVE_SEARCH_API_KEY).
	BraveAPIKeyEnv string
	// TavilyAPIKeyEnv names the env var for Tavily (default TAVILY_API_KEY).
	TavilyAPIKeyEnv string
	// SerpAPIKeyEnv names the env var for SerpAPI (default SERPAPI_KEY).
	SerpAPIKeyEnv string
	// SearXNGURL is a SearXNG base URL (also read from SEARXNG_URL when empty).
	SearXNGURL string
	// FetchMaxBytes caps web_fetch body read (default 64KiB).
	FetchMaxBytes int
}

func (o WebSearchOptions) normalized() WebSearchOptions {
	out := o
	out.Provider = strings.ToLower(strings.TrimSpace(out.Provider))
	if out.Provider == "" {
		out.Provider = "auto"
	}
	if out.MaxResults <= 0 {
		out.MaxResults = defaultWebSearchLimit
	}
	if out.MaxResults > maxWebSearchLimit {
		out.MaxResults = maxWebSearchLimit
	}
	if strings.TrimSpace(out.BraveAPIKeyEnv) == "" {
		out.BraveAPIKeyEnv = "BRAVE_SEARCH_API_KEY"
	}
	if strings.TrimSpace(out.TavilyAPIKeyEnv) == "" {
		out.TavilyAPIKeyEnv = "TAVILY_API_KEY"
	}
	if strings.TrimSpace(out.SerpAPIKeyEnv) == "" {
		out.SerpAPIKeyEnv = "SERPAPI_KEY"
	}
	if strings.TrimSpace(out.SearXNGURL) == "" {
		out.SearXNGURL = strings.TrimSpace(os.Getenv("SEARXNG_URL"))
	}
	if out.FetchMaxBytes <= 0 {
		out.FetchMaxBytes = defaultWebFetchBytes
	}
	if out.FetchMaxBytes > maxWebFetchBytes {
		out.FetchMaxBytes = maxWebFetchBytes
	}
	return out
}

// SearchHit is one ranked web_search result.
type SearchHit struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

type webSearchTool struct {
	cfg        WebSearchOptions
	httpClient *http.Client
}

func (t *webSearchTool) Name() string { return "web_search" }
func (t *webSearchTool) Description() string {
	return "Search the web for a query; returns ranked title/url/snippet results. Provider from config (auto: Brave→Tavily→SerpAPI→SearXNG→DuckDuckGo). Not for blind pre-pass."
}
func (t *webSearchTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"limit":{"type":"integer","description":"max results 1-10"}},"required":["query"]}`)
}
func (t *webSearchTool) Call(ctx context.Context, input string, args json.RawMessage) (Result, error) {
	cfg := t.cfg.normalized()
	q, limit := parseSearchArgs(input, args, cfg.MaxResults)
	if q == "" {
		return fail(t.Name(), fmt.Errorf("query required"))
	}
	if looksLikeProsePath(q) && len(strings.Fields(q)) > 40 {
		return fail(t.Name(), fmt.Errorf("web_search needs a short query (got long prose/goal text)"))
	}
	order, err := searchProviderOrder(cfg)
	if err != nil {
		return fail(t.Name(), err)
	}
	var errs []string
	for _, provider := range order {
		hits, used, serr := t.runSearch(ctx, provider, q, limit, cfg)
		if serr == nil {
			var b strings.Builder
			b.WriteString(fmt.Sprintf("provider=%s query=%q results=%d\n", used, q, len(hits)))
			for i, h := range hits {
				b.WriteString(fmt.Sprintf("%d. %s\n   %s\n   %s\n", i+1, h.Title, h.URL, h.Snippet))
			}
			if len(hits) == 0 {
				b.WriteString("(no results)\n")
			}
			return ok(t.Name(), b.String()), nil
		}
		errs = append(errs, fmt.Sprintf("%s: %v", provider, serr))
	}
	return fail(t.Name(), fmt.Errorf("web_search: all providers failed — %s (set BRAVE_SEARCH_API_KEY / BRAVE_API_KEY, TAVILY_API_KEY, SERPAPI_KEY, or SEARXNG_URL)", strings.Join(errs, "; ")))
}

func parseSearchArgs(input string, args json.RawMessage, defLimit int) (query string, limit int) {
	limit = defLimit
	if len(args) > 0 {
		var m map[string]any
		if json.Unmarshal(args, &m) == nil {
			if q, ok := m["query"].(string); ok {
				query = strings.TrimSpace(q)
			}
			limit = coerceLimit(m["limit"], limit)
		}
	}
	if query == "" {
		in := strings.TrimSpace(input)
		if strings.HasPrefix(in, "{") {
			var m map[string]any
			if json.Unmarshal([]byte(in), &m) == nil {
				if q, ok := m["query"].(string); ok {
					query = strings.TrimSpace(q)
				}
				limit = coerceLimit(m["limit"], limit)
			}
		} else {
			query = firstField(in)
			// Allow "query text\nlimit=3"
			if _, rest, ok := strings.Cut(in, "\n"); ok {
				rest = strings.TrimSpace(rest)
				if strings.HasPrefix(strings.ToLower(rest), "limit=") {
					if n, err := strconv.Atoi(strings.TrimSpace(rest[6:])); err == nil {
						limit = n
					}
				}
			}
		}
	}
	if limit <= 0 {
		limit = defLimit
	}
	if limit > maxWebSearchLimit {
		limit = maxWebSearchLimit
	}
	return query, limit
}

func coerceLimit(v any, def int) int {
	switch n := v.(type) {
	case float64:
		if int(n) > 0 {
			return int(n)
		}
	case int:
		if n > 0 {
			return n
		}
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil && i > 0 {
			return i
		}
	case json.Number:
		if i, err := n.Int64(); err == nil && i > 0 {
			return int(i)
		}
	}
	return def
}

// searchProviderOrder returns providers to try (preferred first, then graceful fallback).
func searchProviderOrder(cfg WebSearchOptions) ([]string, error) {
	p := cfg.Provider
	hasBrave := webSearchAPIKey(cfg.BraveAPIKeyEnv, "BRAVE_API_KEY") != ""
	hasTavily := strings.TrimSpace(os.Getenv(cfg.TavilyAPIKeyEnv)) != ""
	hasSerp := strings.TrimSpace(os.Getenv(cfg.SerpAPIKeyEnv)) != ""
	hasSearx := strings.TrimSpace(cfg.SearXNGURL) != ""

	switch p {
	case "duckduckgo", "ddg":
		return []string{"duckduckgo"}, nil
	case "brave":
		if !hasBrave {
			return nil, fmt.Errorf("web_search provider=brave but %s (or BRAVE_API_KEY) is empty — set it in .env.local", cfg.BraveAPIKeyEnv)
		}
		return keyedThenFallback("brave", hasBrave, hasTavily, hasSerp, hasSearx), nil
	case "tavily":
		if !hasTavily {
			return nil, fmt.Errorf("web_search provider=tavily but %s is empty — set it in .env.local", cfg.TavilyAPIKeyEnv)
		}
		return keyedThenFallback("tavily", hasBrave, hasTavily, hasSerp, hasSearx), nil
	case "serpapi", "serp":
		if !hasSerp {
			return nil, fmt.Errorf("web_search provider=serpapi but %s is empty — set it in .env.local", cfg.SerpAPIKeyEnv)
		}
		return keyedThenFallback("serpapi", hasBrave, hasTavily, hasSerp, hasSearx), nil
	case "searxng":
		if !hasSearx {
			return nil, fmt.Errorf("web_search provider=searxng but searxng_url / SEARXNG_URL is empty")
		}
		return keyedThenFallback("searxng", hasBrave, hasTavily, hasSerp, hasSearx), nil
	case "auto", "":
		return autoProviderOrder(hasBrave, hasTavily, hasSerp, hasSearx), nil
	default:
		return nil, fmt.Errorf("unknown web_search provider %q (use auto|duckduckgo|brave|tavily|serpapi|searxng)", p)
	}
}

func autoProviderOrder(hasBrave, hasTavily, hasSerp, hasSearx bool) []string {
	var out []string
	if hasBrave {
		out = append(out, "brave")
	}
	if hasTavily {
		out = append(out, "tavily")
	}
	if hasSerp {
		out = append(out, "serpapi")
	}
	if hasSearx {
		out = append(out, "searxng")
	}
	out = append(out, "duckduckgo")
	return out
}

// keyedThenFallback puts preferred first, then other keyed providers, then DDG.
func keyedThenFallback(preferred string, hasBrave, hasTavily, hasSerp, hasSearx bool) []string {
	seen := map[string]bool{preferred: true}
	out := []string{preferred}
	add := func(name string, ok bool) {
		if ok && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	add("brave", hasBrave)
	add("tavily", hasTavily)
	add("serpapi", hasSerp)
	add("searxng", hasSearx)
	out = append(out, "duckduckgo")
	return out
}

func webSearchAPIKey(primaryEnv, fallbackEnv string) string {
	if v := strings.TrimSpace(os.Getenv(primaryEnv)); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv(fallbackEnv))
}

func (t *webSearchTool) client() *http.Client {
	if t.httpClient != nil {
		return t.httpClient
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (t *webSearchTool) runSearch(ctx context.Context, provider, query string, limit int, cfg WebSearchOptions) ([]SearchHit, string, error) {
	switch provider {
	case "brave":
		hits, err := searchBrave(ctx, t.client(), webSearchAPIKey(cfg.BraveAPIKeyEnv, "BRAVE_API_KEY"), query, limit)
		return hits, "brave", err
	case "tavily":
		hits, err := searchTavily(ctx, t.client(), strings.TrimSpace(os.Getenv(cfg.TavilyAPIKeyEnv)), query, limit)
		return hits, "tavily", err
	case "serpapi":
		hits, err := searchSerpAPI(ctx, t.client(), strings.TrimSpace(os.Getenv(cfg.SerpAPIKeyEnv)), query, limit)
		return hits, "serpapi", err
	case "searxng":
		hits, err := searchSearXNG(ctx, t.client(), cfg.SearXNGURL, query, limit)
		return hits, "searxng", err
	default:
		hits, err := searchDuckDuckGo(ctx, t.client(), query, limit)
		return hits, "duckduckgo", err
	}
}

func searchBrave(ctx context.Context, client *http.Client, key, query string, limit int) ([]SearchHit, error) {
	u := "https://api.search.brave.com/res/v1/web/search?q=" + url.QueryEscape(query) + "&count=" + strconv.Itoa(limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", key)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("brave search http %d: %s", resp.StatusCode, truncateErrBody(body))
	}
	return parseBraveJSON(body, limit)
}

func parseBraveJSON(body []byte, limit int) ([]SearchHit, error) {
	var parsed struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("brave search decode: %w", err)
	}
	var hits []SearchHit
	for _, r := range parsed.Web.Results {
		if len(hits) >= limit {
			break
		}
		hits = append(hits, SearchHit{Title: r.Title, URL: r.URL, Snippet: r.Description})
	}
	return hits, nil
}

func searchTavily(ctx context.Context, client *http.Client, key, query string, limit int) ([]SearchHit, error) {
	payload, _ := json.Marshal(map[string]any{
		"api_key":        key,
		"query":          query,
		"max_results":    limit,
		"include_answer": false,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.tavily.com/search", strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("tavily search http %d: %s", resp.StatusCode, truncateErrBody(body))
	}
	var parsed struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("tavily search decode: %w", err)
	}
	var hits []SearchHit
	for _, r := range parsed.Results {
		if len(hits) >= limit {
			break
		}
		hits = append(hits, SearchHit{Title: r.Title, URL: r.URL, Snippet: r.Content})
	}
	return hits, nil
}

func searchSerpAPI(ctx context.Context, client *http.Client, key, query string, limit int) ([]SearchHit, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("SERPAPI_KEY empty")
	}
	u := fmt.Sprintf("https://serpapi.com/search.json?engine=google&q=%s&num=%d&api_key=%s",
		url.QueryEscape(query), limit, url.QueryEscape(key))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("serpapi http %d: %s", resp.StatusCode, truncateErrBody(body))
	}
	var parsed struct {
		Organic []struct {
			Title   string `json:"title"`
			Link    string `json:"link"`
			Snippet string `json:"snippet"`
		} `json:"organic_results"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("serpapi decode: %w", err)
	}
	var hits []SearchHit
	for _, r := range parsed.Organic {
		if len(hits) >= limit {
			break
		}
		hits = append(hits, SearchHit{Title: r.Title, URL: r.Link, Snippet: r.Snippet})
	}
	return hits, nil
}

func searchSearXNG(ctx context.Context, client *http.Client, base, query string, limit int) ([]SearchHit, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	u := base + "/search?q=" + url.QueryEscape(query) + "&format=json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "GliderBot/1.0 (+web_search)")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("searxng http %d: %s", resp.StatusCode, truncateErrBody(body))
	}
	var parsed struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("searxng decode: %w", err)
	}
	var hits []SearchHit
	for _, r := range parsed.Results {
		if len(hits) >= limit {
			break
		}
		hits = append(hits, SearchHit{Title: r.Title, URL: r.URL, Snippet: r.Content})
	}
	return hits, nil
}

// searchDuckDuckGo uses the no-key HTML endpoint and parses result anchors.
func searchDuckDuckGo(ctx context.Context, client *http.Client, query string, limit int) ([]SearchHit, error) {
	u := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "GliderBot/1.0 (+https://github.com/glider-ai/glider; web_search)")
	req.Header.Set("Accept", "text/html")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("duckduckgo http %d: %s — set BRAVE_SEARCH_API_KEY or TAVILY_API_KEY for a keyed provider", resp.StatusCode, truncateErrBody(body))
	}
	hits := parseDuckDuckGoHTML(string(body), limit)
	if len(hits) == 0 {
		return nil, fmt.Errorf("duckduckgo returned no parseable results (blocked or empty) — set BRAVE_SEARCH_API_KEY, TAVILY_API_KEY, or SEARXNG_URL")
	}
	return hits, nil
}

var (
	ddgResultRE = regexp.MustCompile(`(?is)<a[^>]*class="[^"]*result__a[^"]*"[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	ddgSnipRE   = regexp.MustCompile(`(?is)<a[^>]*class="[^"]*result__snippet[^"]*"[^>]*>(.*?)</a>|<td[^>]*class="[^"]*result-snippet[^"]*"[^>]*>(.*?)</td>`)
	tagRE       = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>|<style[^>]*>.*?</style>|<[^>]+>`)
	spaceRE     = regexp.MustCompile(`[ \t\x0a\x0d]{2,}`)
)

// parseDuckDuckGoHTML extracts title/url/snippet from DDG HTML (exported for tests via package).
func parseDuckDuckGoHTML(page string, limit int) []SearchHit {
	if limit <= 0 {
		limit = defaultWebSearchLimit
	}
	matches := ddgResultRE.FindAllStringSubmatch(page, -1)
	snips := ddgSnipRE.FindAllStringSubmatch(page, -1)
	var hits []SearchHit
	for i, m := range matches {
		if len(hits) >= limit {
			break
		}
		rawURL := html.UnescapeString(strings.TrimSpace(m[1]))
		title := strings.TrimSpace(stripTags(html.UnescapeString(m[2])))
		if title == "" {
			continue
		}
		finalURL := unwrapDDGHref(rawURL)
		if finalURL == "" || strings.HasPrefix(finalURL, "/") {
			continue
		}
		snip := ""
		if i < len(snips) {
			for _, g := range snips[i][1:] {
				if strings.TrimSpace(g) != "" {
					snip = strings.TrimSpace(stripTags(html.UnescapeString(g)))
					break
				}
			}
		}
		hits = append(hits, SearchHit{Title: title, URL: finalURL, Snippet: snip})
	}
	return hits
}

func unwrapDDGHref(href string) string {
	href = strings.TrimSpace(href)
	if href == "" {
		return ""
	}
	// //duckduckgo.com/l/?uddg=<urlencoded>
	if u, err := url.Parse(href); err == nil {
		if q := u.Query().Get("uddg"); q != "" {
			if decoded, err := url.QueryUnescape(q); err == nil {
				return decoded
			}
			return q
		}
		if u.Scheme == "" && strings.HasPrefix(href, "//") {
			return "https:" + href
		}
		if u.Scheme == "http" || u.Scheme == "https" {
			return href
		}
	}
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return href
	}
	return href
}
