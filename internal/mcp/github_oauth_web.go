package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	githubAuthorizeURL = "https://github.com/login/oauth/authorize"
	// DefaultOAuthCallbackPath must match the Authorization callback URL on the GitHub OAuth App.
	DefaultOAuthCallbackPath = "/oauth/callback"
)

var (
	oauthMu     sync.Mutex
	oauthStates = map[string]time.Time{} // state → expiry
)

// OAuthAuthorizeStart is returned so the dashboard can open GitHub's consent page.
type OAuthAuthorizeStart struct {
	AuthorizeURL string `json:"authorize_url"`
	State        string `json:"state"`
	CallbackPath string `json:"callback_path"`
	Message      string `json:"message,omitempty"`
}

// DefaultOAuthRedirectURI builds http://127.0.0.1:<dashPort>/oauth/callback.
// Override with GLIDER_GITHUB_OAUTH_REDIRECT_URI if needed.
func DefaultOAuthRedirectURI(dashboardBase string) string {
	if v := strings.TrimSpace(osGetenv("GLIDER_GITHUB_OAUTH_REDIRECT_URI")); v != "" {
		return v
	}
	base := strings.TrimRight(strings.TrimSpace(dashboardBase), "/")
	if base == "" {
		base = "http://127.0.0.1:8081"
	}
	return base + DefaultOAuthCallbackPath
}

func osGetenv(k string) string { return strings.TrimSpace(lookupEnv(k)) }

func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// StartGitHubOAuthAuthorize begins the standard OAuth App web flow (code + client secret).
// This is what classic GitHub OAuth Apps expect — not device flow.
func StartGitHubOAuthAuthorize(dashboardBase string) (OAuthAuthorizeStart, error) {
	clientID := ResolveGitHubOAuthClientID()
	if clientID == "" {
		return OAuthAuthorizeStart{}, fmt.Errorf("set GLIDER_GITHUB_OAUTH_CLIENT_ID in .env.local")
	}
	if ResolveGitHubOAuthClientSecret() == "" {
		return OAuthAuthorizeStart{}, fmt.Errorf("set GLIDER_GITHUB_OAUTH_CLIENT_SECRET in .env.local (required for OAuth App web login)")
	}
	state, err := randomState()
	if err != nil {
		return OAuthAuthorizeStart{}, err
	}
	oauthMu.Lock()
	oauthStates[state] = time.Now().Add(10 * time.Minute)
	// prune
	now := time.Now()
	for k, exp := range oauthStates {
		if now.After(exp) {
			delete(oauthStates, k)
		}
	}
	oauthMu.Unlock()

	redirect := DefaultOAuthRedirectURI(dashboardBase)
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirect)
	q.Set("scope", ResolveGitHubOAuthScopes())
	q.Set("state", state)
	authURL := githubAuthorizeURL + "?" + q.Encode()
	return OAuthAuthorizeStart{
		AuthorizeURL: authURL,
		State:        state,
		CallbackPath: DefaultOAuthCallbackPath,
		Message:      "Continue in the browser to authorize Glider, then you will return to the dashboard.",
	}, nil
}

// ExchangeGitHubOAuthCode validates state and exchanges the authorization code for a token.
func ExchangeGitHubOAuthCode(ctx context.Context, code, state, dashboardBase string) (string, error) {
	code = strings.TrimSpace(code)
	state = strings.TrimSpace(state)
	if code == "" || state == "" {
		return "", fmt.Errorf("missing code or state")
	}
	oauthMu.Lock()
	exp, ok := oauthStates[state]
	delete(oauthStates, state)
	oauthMu.Unlock()
	if !ok || time.Now().After(exp) {
		return "", fmt.Errorf("invalid or expired OAuth state — start Sign in again")
	}
	clientID := ResolveGitHubOAuthClientID()
	secret := ResolveGitHubOAuthClientSecret()
	if clientID == "" || secret == "" {
		return "", fmt.Errorf("OAuth client id/secret not configured")
	}
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", secret)
	form.Set("code", code)
	form.Set("redirect_uri", DefaultOAuthRedirectURI(dashboardBase))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubAccessTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var parsed struct {
		AccessToken      string `json:"access_token"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("token parse: %w (%s)", err, truncate(string(body), 200))
	}
	if parsed.Error != "" {
		return "", fmt.Errorf("%s: %s", parsed.Error, parsed.ErrorDescription)
	}
	if parsed.AccessToken == "" {
		return "", fmt.Errorf("empty access_token from GitHub")
	}
	return parsed.AccessToken, nil
}
