// Package youtube ports adapters/youtube-oauth-target.ts +
// services/youtube-oauth.ts: standard Google OAuth2 plus the documented
// YouTube Data API v3, both plain REST - no googleapis/google-auth-library
// SDK needed, same as every other OAuth target in this rewrite.
package youtube

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

const (
	authURL = "https://accounts.google.com/o/oauth2/v2/auth"
	apiBase = "https://www.googleapis.com/youtube/v3"
	scope   = "https://www.googleapis.com/auth/youtube.force-ssl"
)

// tokenURL/revokeURL are vars (not consts) so tests can point them at an
// httptest.Server instead of hitting Google for real.
var (
	tokenURL  = "https://oauth2.googleapis.com/token"
	revokeURL = "https://oauth2.googleapis.com/revoke"
)

type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string
}

func (c OAuthConfig) IsConfigured() bool { return c.ClientID != "" && c.ClientSecret != "" }

func (c OAuthConfig) AuthorizeURL(state string) string {
	params := url.Values{
		"client_id": {c.ClientID}, "redirect_uri": {c.RedirectURI}, "response_type": {"code"},
		"access_type": {"offline"}, "scope": {scope}, "state": {state}, "prompt": {"consent"},
	}
	return authURL + "?" + params.Encode()
}

type GoogleTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

func (c OAuthConfig) ExchangeCode(code string) (*GoogleTokens, error) {
	form := url.Values{
		"client_id": {c.ClientID}, "client_secret": {c.ClientSecret},
		"code": {code}, "redirect_uri": {c.RedirectURI}, "grant_type": {"authorization_code"},
	}
	return c.requestToken(form)
}

func (c OAuthConfig) RefreshToken(refreshToken string) (*GoogleTokens, error) {
	form := url.Values{
		"client_id": {c.ClientID}, "client_secret": {c.ClientSecret},
		"refresh_token": {refreshToken}, "grant_type": {"refresh_token"},
	}
	tokens, err := c.requestToken(form)
	if err != nil {
		return nil, err
	}
	if tokens.RefreshToken == "" {
		tokens.RefreshToken = refreshToken // Google omits it when the old one is still valid
	}
	return tokens, nil
}

// TokenError is returned by requestToken for a non-2xx response, carrying
// the status code so a caller (GetValidAccessToken) can tell a rejected
// refresh token (4xx - Google looked at it and said no) apart from a
// transient outage (network error, or a 5xx from Google's own token
// endpoint) - only the former means the stored refresh token is actually
// dead and the connection should be deleted.
type TokenError struct {
	StatusCode int
	Err        error
}

func (e *TokenError) Error() string { return e.Err.Error() }
func (e *TokenError) Unwrap() error { return e.Err }

// Permanent reports whether Google rejected the request outright (the
// refresh token itself is bad) rather than merely being unavailable.
func (e *TokenError) Permanent() bool { return e.StatusCode >= 400 && e.StatusCode < 500 }

func (c OAuthConfig) requestToken(form url.Values) (*GoogleTokens, error) {
	resp, err := http.PostForm(tokenURL, form)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var errBody struct {
			ErrorDescription string `json:"error_description"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		msg := fmt.Sprintf("token request failed: status %d", resp.StatusCode)
		if errBody.ErrorDescription != "" {
			msg = errBody.ErrorDescription
		}
		return nil, &TokenError{StatusCode: resp.StatusCode, Err: fmt.Errorf("%s", msg)}
	}
	var tokens GoogleTokens
	if err := json.NewDecoder(resp.Body).Decode(&tokens); err != nil {
		return nil, err
	}
	if tokens.AccessToken == "" {
		return nil, fmt.Errorf("no access token received from Google")
	}
	return &tokens, nil
}

func (c OAuthConfig) Revoke(accessToken string) {
	// Best-effort, matching the Node service: a failed revoke (token already
	// invalid) is logged there and swallowed here too - the DB row is
	// deleted regardless.
	resp, err := http.PostForm(revokeURL, url.Values{"token": {accessToken}})
	if err == nil {
		resp.Body.Close()
	}
}

func ExpiresAtFrom(tokens *GoogleTokens) time.Time {
	return time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
}
