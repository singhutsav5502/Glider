package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type webFetchTool struct {
	allowHosts []string
	maxBytes   int
	httpClient *http.Client
}

func (t *webFetchTool) Name() string { return "web_fetch" }
func (t *webFetchTool) Description() string {
	return "Fetch a URL and return readable plain text (HTML stripped, size-capped). Honors host allowlist when configured. Prefer over http_fetch for reading pages; use http_fetch for raw status+body."
}
func (t *webFetchTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]}`)
}
func (t *webFetchTool) Call(ctx context.Context, input string, args json.RawMessage) (Result, error) {
	u := parseURLArg(input, args)
	if u == "" || !strings.HasPrefix(u, "http") {
		return fail(t.Name(), fmt.Errorf("url required (http/https)"))
	}
	if err := checkHostAllowed(u, t.allowHosts); err != nil {
		return fail(t.Name(), err)
	}
	maxBytes := t.maxBytes
	if maxBytes <= 0 {
		maxBytes = defaultWebFetchBytes
	}
	client := t.httpClient
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fail(t.Name(), err)
	}
	req.Header.Set("User-Agent", "GliderBot/1.0 (+https://github.com/glider-ai/glider; web_fetch)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,text/plain;q=0.8,*/*;q=0.5")
	resp, err := client.Do(req)
	if err != nil {
		return fail(t.Name(), err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
	truncated := false
	if len(raw) > maxBytes {
		raw = raw[:maxBytes]
		truncated = true
	}
	ct := resp.Header.Get("Content-Type")
	text := string(raw)
	if strings.Contains(strings.ToLower(ct), "html") || looksLikeHTML(text) {
		text = htmlToReadableText(text)
	}
	text = strings.TrimSpace(text)
	if len(text) > maxBytes {
		text = text[:maxBytes] + "\n...truncated"
		truncated = true
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("status=%d content_type=%s truncated=%t bytes=%d\n", resp.StatusCode, ct, truncated, len(raw)))
	b.WriteString(text)
	if resp.StatusCode >= 400 {
		return Result{Name: t.Name(), Kind: KindBuiltin, OK: false, Output: b.String(), Err: fmt.Sprintf("http %d", resp.StatusCode)}, nil
	}
	return ok(t.Name(), b.String()), nil
}
