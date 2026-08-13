package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Device flow endpoints (github.com). Override host via GITHUB_HOST if needed later.
const (
	githubDeviceCodeURL = "https://github.com/login/device/code"
	githubAccessTokenURL = "https://github.com/login/oauth/access_token"
	githubDeviceVerifyURL = "https://github.com/login/device"
)

// DefaultGitHubOAuthScopes for MCP-oriented access (user can narrow via env).
const DefaultGitHubOAuthScopes = "repo read:org read:user"

// ResolveGitHubOAuthClientID returns client id for device flow (optional secret).
func ResolveGitHubOAuthClientID() string {
	for _, k := range []string{"GLIDER_GITHUB_OAUTH_CLIENT_ID", "GITHUB_OAUTH_CLIENT_ID"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func ResolveGitHubOAuthClientSecret() string {
	for _, k := range []string{"GLIDER_GITHUB_OAUTH_CLIENT_SECRET", "GITHUB_OAUTH_CLIENT_SECRET"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func ResolveGitHubOAuthScopes() string {
	if v := strings.TrimSpace(os.Getenv("GITHUB_OAUTH_SCOPES")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("GLIDER_GITHUB_OAUTH_SCOPES")); v != "" {
		return v
	}
	return DefaultGitHubOAuthScopes
}

// DeviceStart carries what the dashboard shows for "enter this code", in the
// style of Cursor.
type DeviceStart struct {
	ClientID                string `json:"client_id,omitempty"`
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete,omitempty"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
	Message                 string `json:"message,omitempty"`
}

// DevicePollResult carries the result while the code polls, and after a
// person authorizes it.
type DevicePollResult struct {
	Status       string `json:"status"` // pending | slow_down | authorized | error | expired
	AccessToken  string `json:"-"`      // never JSON to client
	TokenType    string `json:"token_type,omitempty"`
	Scope        string `json:"scope,omitempty"`
	Error        string `json:"error,omitempty"`
	ErrorDesc    string `json:"error_description,omitempty"`
	Connected    bool   `json:"connected,omitempty"`
	HTTPOK       bool   `json:"http_connected,omitempty"`
}

type deviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
	Error                   string `json:"error"`
	ErrorDescription        string `json:"error_description"`
}

type deviceTokenResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	Scope            string `json:"scope"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	Interval         int    `json:"interval"`
}

var (
	deviceMu    sync.Mutex
	devicePending = map[string]devicePendingEntry{}
)

type devicePendingEntry struct {
	ClientID string
	Secret   string
	Interval int
	Expires  time.Time
}

// StartGitHubDeviceFlow begins GitHub OAuth device authorization (Cursor-like).
// Requires GLIDER_GITHUB_OAUTH_CLIENT_ID or GITHUB_OAUTH_CLIENT_ID (OAuth/GitHub App with Device Flow enabled).
func StartGitHubDeviceFlow(ctx context.Context) (DeviceStart, error) {
	clientID := ResolveGitHubOAuthClientID()
	if clientID == "" {
		return DeviceStart{}, fmt.Errorf("github device flow: set GLIDER_GITHUB_OAUTH_CLIENT_ID (or GITHUB_OAUTH_CLIENT_ID) to your GitHub OAuth/GitHub App client id with Device Flow enabled")
	}
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("scope", ResolveGitHubOAuthScopes())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubDeviceCodeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return DeviceStart{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return DeviceStart{}, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == http.StatusNotFound {
		return DeviceStart{}, fmt.Errorf("GitHub device endpoint returned 404 — enable Device Flow on the app (OAuth App → Enable Device Flow, or GitHub App → Device Flow), or use browser Sign in (needs CLIENT_SECRET) / Paste PAT")
	}
	if res.StatusCode >= 400 {
		return DeviceStart{}, fmt.Errorf("GitHub device/code HTTP %d: %s", res.StatusCode, truncate(string(body), 300))
	}
	var parsed deviceCodeResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return DeviceStart{}, fmt.Errorf("device code parse: %w (%s)", err, truncate(string(body), 200))
	}
	if parsed.Error != "" {
		return DeviceStart{}, fmt.Errorf("%s: %s", parsed.Error, parsed.ErrorDescription)
	}
	if parsed.DeviceCode == "" || parsed.UserCode == "" {
		return DeviceStart{}, fmt.Errorf("device code response incomplete: %s", truncate(string(body), 200))
	}
	if parsed.VerificationURI == "" {
		parsed.VerificationURI = githubDeviceVerifyURL
	}
	if parsed.Interval <= 0 {
		parsed.Interval = 5
	}
	deviceMu.Lock()
	devicePending[parsed.DeviceCode] = devicePendingEntry{
		ClientID: clientID,
		Secret:   ResolveGitHubOAuthClientSecret(),
		Interval: parsed.Interval,
		Expires:  time.Now().Add(time.Duration(parsed.ExpiresIn) * time.Second),
	}
	deviceMu.Unlock()
	msg := fmt.Sprintf("Open %s and enter code %s (or use the complete link).", parsed.VerificationURI, parsed.UserCode)
	return DeviceStart{
		ClientID:                clientID,
		DeviceCode:              parsed.DeviceCode,
		UserCode:                parsed.UserCode,
		VerificationURI:         parsed.VerificationURI,
		VerificationURIComplete: parsed.VerificationURIComplete,
		ExpiresIn:               parsed.ExpiresIn,
		Interval:                parsed.Interval,
		Message:                 msg,
	}, nil
}

// PollGitHubDeviceFlow checks whether the user finished authorizing.
func PollGitHubDeviceFlow(ctx context.Context, deviceCode string) (DevicePollResult, error) {
	deviceCode = strings.TrimSpace(deviceCode)
	if deviceCode == "" {
		return DevicePollResult{Status: "error", Error: "missing device_code"}, nil
	}
	deviceMu.Lock()
	ent, ok := devicePending[deviceCode]
	deviceMu.Unlock()
	if !ok {
		return DevicePollResult{Status: "error", Error: "unknown or expired device_code — start again"}, nil
	}
	if time.Now().After(ent.Expires) {
		deviceMu.Lock()
		delete(devicePending, deviceCode)
		deviceMu.Unlock()
		return DevicePollResult{Status: "expired", Error: "expired"}, nil
	}
	form := url.Values{}
	form.Set("client_id", ent.ClientID)
	form.Set("device_code", deviceCode)
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	if ent.Secret != "" {
		form.Set("client_secret", ent.Secret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubAccessTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return DevicePollResult{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return DevicePollResult{}, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var parsed deviceTokenResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return DevicePollResult{}, fmt.Errorf("token parse: %w", err)
	}
	switch parsed.Error {
	case "":
		if parsed.AccessToken == "" {
			return DevicePollResult{Status: "error", Error: "empty access_token"}, nil
		}
		deviceMu.Lock()
		delete(devicePending, deviceCode)
		deviceMu.Unlock()
		return DevicePollResult{
			Status:      "authorized",
			AccessToken: parsed.AccessToken,
			TokenType:   parsed.TokenType,
			Scope:       parsed.Scope,
		}, nil
	case "authorization_pending":
		return DevicePollResult{Status: "pending"}, nil
	case "slow_down":
		return DevicePollResult{Status: "slow_down"}, nil
	case "expired_token", "access_denied":
		deviceMu.Lock()
		delete(devicePending, deviceCode)
		deviceMu.Unlock()
		return DevicePollResult{Status: "expired", Error: parsed.Error, ErrorDesc: parsed.ErrorDescription}, nil
	default:
		return DevicePollResult{Status: "error", Error: parsed.Error, ErrorDesc: parsed.ErrorDescription}, nil
	}
}

// ApplyGitHubTokenAndConnect saves the token and connects the HTTP github server.
func (m *Manager) ApplyGitHubTokenAndConnect(ctx context.Context, token string) error {
	if err := SaveGitHubToken(token); err != nil {
		return err
	}
	if m == nil {
		return nil
	}
	cfg := DefaultGitHubConfig()
	_, err := m.Connect(ctx, cfg)
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
