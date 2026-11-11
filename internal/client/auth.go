package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// TokenSource returns a valid OAuth access token.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
	// Invalidate drops any cached token so the next call to Token fetches a new one.
	Invalidate()
}

// StaticToken is a TokenSource that always returns the same token.
type StaticToken string

func (s StaticToken) Token(context.Context) (string, error) { return string(s), nil }
func (s StaticToken) Invalidate()                           {}

// RefreshTokenSource exchanges a Zoho OAuth refresh token for short-lived access
// tokens, caching them until shortly before they expire.
//
// See https://www.zoho.com/accounts/protocol/oauth/web-apps/access-token-expiry.html
type RefreshTokenSource struct {
	AccountsURL  string
	ClientID     string
	ClientSecret string
	RefreshToken string
	HTTPClient   *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
	now     func() time.Time
}

type zohoTokenResponse struct {
	AccessToken      string `json:"access_token"`
	ExpiresIn        int64  `json:"expires_in"`
	APIDomain        string `json:"api_domain"`
	TokenType        string `json:"token_type"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// expiryLeeway is subtracted from the token lifetime so a token is never used
// right at the edge of its validity.
const expiryLeeway = 2 * time.Minute

func (r *RefreshTokenSource) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func (r *RefreshTokenSource) Invalidate() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.token = ""
	r.expires = time.Time{}
}

func (r *RefreshTokenSource) Token(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.token != "" && r.clock().Before(r.expires) {
		return r.token, nil
	}

	form := url.Values{
		"refresh_token": {r.RefreshToken},
		"client_id":     {r.ClientID},
		"client_secret": {r.ClientSecret},
		"grant_type":    {"refresh_token"},
	}
	endpoint := strings.TrimRight(r.AccountsURL, "/") + "/oauth/v2/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	hc := r.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("requesting Zoho access token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var tr zohoTokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("decoding Zoho token response (HTTP %d): %w", resp.StatusCode, err)
	}
	// Zoho returns HTTP 200 with an "error" field for invalid credentials.
	if tr.Error != "" {
		if strings.Contains(strings.ToLower(tr.ErrorDescription), "too many requests") {
			// Zoho limits how many access tokens a refresh token gets in a
			// short time; every provider run asks for one.
			return "", fmt.Errorf("zoho is rate-limiting access tokens: %q. Wait a few minutes and run again", tr.ErrorDescription)
		}
		msg := fmt.Sprintf("zoho token endpoint returned error %q", tr.Error)
		if tr.ErrorDescription != "" {
			msg += ": " + tr.ErrorDescription
		}
		return "", fmt.Errorf("%s (check client_id, client_secret, refresh_token and that the accounts URL matches your region)", msg)
	}
	if resp.StatusCode != http.StatusOK || tr.AccessToken == "" {
		return "", fmt.Errorf("zoho token endpoint returned HTTP %d without an access token", resp.StatusCode)
	}

	lifetime := time.Duration(tr.ExpiresIn) * time.Second
	if lifetime <= 0 {
		lifetime = time.Hour
	}
	if lifetime > 2*expiryLeeway {
		lifetime -= expiryLeeway
	}
	r.token = tr.AccessToken
	r.expires = r.clock().Add(lifetime)
	return r.token, nil
}
