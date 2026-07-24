package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

type shellExec struct {
	root      string
	allow     bool
	allowList []string
}

func (t *shellExec) Name() string        { return "shell_exec" }
func (t *shellExec) Description() string { return "Run an allowlisted shell command in the workspace" }
func (t *shellExec) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`)
}
func (t *shellExec) Call(ctx context.Context, input string, _ json.RawMessage) (Result, error) {
	if !t.allow {
		return Result{Name: t.Name(), Kind: KindBuiltin, OK: false, Err: "shell_exec disabled"}, fmt.Errorf("shell disabled")
	}
	line := strings.TrimSpace(input)
	if line == "" {
		return fail(t.Name(), fmt.Errorf("command required"))
	}
	parts := strings.Fields(line)
	cmdName := parts[0]
	if len(t.allowList) > 0 && !containsFold(t.allowList, cmdName) {
		return fail(t.Name(), fmt.Errorf("command %q not allowlisted", cmdName))
	}
	c := exec.CommandContext(ctx, parts[0], parts[1:]...)
	if t.root != "" {
		c.Dir = t.root
	}
	out, err := c.CombinedOutput()
	text := string(out)
	if len(text) > 32<<10 {
		text = text[:32<<10] + "\n...truncated"
	}
	if err != nil {
		return Result{Name: t.Name(), Kind: KindBuiltin, OK: false, Output: text, Err: err.Error()}, nil
	}
	return ok(t.Name(), text), nil
}

type httpFetch struct {
	allowHosts []string
	timeout    time.Duration
}

func (t *httpFetch) Name() string { return "http_fetch" }
func (t *httpFetch) Description() string {
	return "HTTP GET a URL and return raw status+body (optional host allowlist). Prefer web_fetch for readable page text; use web_search for query→ranked results."
}
func (t *httpFetch) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]}`)
}
func (t *httpFetch) Call(ctx context.Context, input string, args json.RawMessage) (Result, error) {
	u := parseURLArg(input, args)
	if u == "" || !strings.HasPrefix(u, "http") {
		return fail(t.Name(), fmt.Errorf("url required"))
	}
	if err := checkHostAllowed(u, t.allowHosts); err != nil {
		return fail(t.Name(), err)
	}
	to := t.timeout
	if to <= 0 {
		to = 20 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u, nil)
	if err != nil {
		return fail(t.Name(), err)
	}
	req.Header.Set("User-Agent", "GliderBot/1.0 (+http_fetch)")
	client := &http.Client{Timeout: to}
	resp, err := client.Do(req)
	if err != nil {
		return fail(t.Name(), err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return ok(t.Name(), fmt.Sprintf("status=%d\n%s", resp.StatusCode, string(b))), nil
}
