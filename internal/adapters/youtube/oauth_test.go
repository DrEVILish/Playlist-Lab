package youtube

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// withTokenServer points the package-level tokenURL at an httptest.Server
// for the duration of the test, restoring it afterward - lets us exercise
// requestToken/ExchangeCode/RefreshToken without hitting Google.
func withTokenServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	orig := tokenURL
	tokenURL = srv.URL
	t.Cleanup(func() { tokenURL = orig })
}

func TestExchangeCode_Success(t *testing.T) {
	withTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if r.FormValue("grant_type") != "authorization_code" || r.FormValue("code") != "auth-code" {
			t.Fatalf("unexpected form: %v", r.Form)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access-token-abc", "refresh_token": "refresh-token-xyz", "expires_in": 3600,
		})
	})

	cfg := OAuthConfig{ClientID: "id", ClientSecret: "secret", RedirectURI: "http://localhost/cb"}
	tokens, err := cfg.ExchangeCode("auth-code")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokens.AccessToken != "access-token-abc" || tokens.RefreshToken != "refresh-token-xyz" || tokens.ExpiresIn != 3600 {
		t.Fatalf("unexpected tokens: %+v", tokens)
	}
}

func TestExchangeCode_NoAccessToken(t *testing.T) {
	withTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{})
	})

	cfg := OAuthConfig{ClientID: "id", ClientSecret: "secret"}
	_, err := cfg.ExchangeCode("auth-code")
	if err == nil || err.Error() != "no access token received from Google" {
		t.Fatalf("expected 'no access token received' error, got %v", err)
	}
}

func TestExchangeCode_ErrorResponseUsesGoogleDescription(t *testing.T) {
	withTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant", "error_description": "Malformed auth code"})
	})

	cfg := OAuthConfig{ClientID: "id", ClientSecret: "secret"}
	_, err := cfg.ExchangeCode("bad-code")
	if err == nil || err.Error() != "Malformed auth code" {
		t.Fatalf("expected Google's error_description surfaced, got %v", err)
	}
}

func TestExchangeCode_ErrorResponseWithoutDescriptionFallsBackToStatus(t *testing.T) {
	withTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	cfg := OAuthConfig{ClientID: "id", ClientSecret: "secret"}
	_, err := cfg.ExchangeCode("code")
	if err == nil || err.Error() != "token request failed: status 500" {
		t.Fatalf("expected status-based fallback error, got %v", err)
	}
}

func TestRefreshToken_KeepsOldRefreshTokenWhenGoogleOmitsIt(t *testing.T) {
	withTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.FormValue("grant_type") != "refresh_token" || r.FormValue("refresh_token") != "old-refresh" {
			t.Fatalf("unexpected form: %v", r.Form)
		}
		// Google omits refresh_token in the response when the old one is still valid.
		json.NewEncoder(w).Encode(map[string]any{"access_token": "new-access", "expires_in": 3600})
	})

	cfg := OAuthConfig{ClientID: "id", ClientSecret: "secret"}
	tokens, err := cfg.RefreshToken("old-refresh")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokens.RefreshToken != "old-refresh" {
		t.Fatalf("expected old refresh token to be preserved, got %q", tokens.RefreshToken)
	}
}

func TestRefreshToken_UsesNewRefreshTokenWhenGoogleReturnsOne(t *testing.T) {
	withTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-access", "refresh_token": "new-refresh", "expires_in": 3600,
		})
	})

	cfg := OAuthConfig{ClientID: "id", ClientSecret: "secret"}
	tokens, err := cfg.RefreshToken("old-refresh")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokens.RefreshToken != "new-refresh" {
		t.Fatalf("expected the fresh refresh token, got %q", tokens.RefreshToken)
	}
}

func TestRefreshToken_PropagatesFailure(t *testing.T) {
	withTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{"error_description": "invalid_grant"})
	})

	cfg := OAuthConfig{ClientID: "id", ClientSecret: "secret"}
	_, err := cfg.RefreshToken("stale-refresh")
	if err == nil || err.Error() != "invalid_grant" {
		t.Fatalf("expected the refresh error to propagate, got %v", err)
	}
}

// ExpiresAtFrom is the sole conversion from Google's relative "expires_in
// seconds" into the absolute timestamp everything else (GetValidAccessToken's
// expiry check) compares against - worth pinning independently of any HTTP call.
func TestExpiresAtFrom(t *testing.T) {
	before := time.Now()
	got := ExpiresAtFrom(&GoogleTokens{ExpiresIn: 3600})
	after := time.Now()

	if got.Before(before.Add(3599*time.Second)) || got.After(after.Add(3601*time.Second)) {
		t.Fatalf("expected ~1 hour from now, got %v (before=%v after=%v)", got, before, after)
	}
}

func TestExpiresAtFrom_ZeroExpiresInIsAlreadyExpired(t *testing.T) {
	got := ExpiresAtFrom(&GoogleTokens{ExpiresIn: 0})
	if got.After(time.Now().Add(time.Second)) {
		t.Fatalf("expected an expires_in of 0 to resolve to ~now, got %v", got)
	}
}

func TestOAuthConfig_IsConfigured(t *testing.T) {
	cases := []struct {
		name string
		cfg  OAuthConfig
		want bool
	}{
		{"both set", OAuthConfig{ClientID: "id", ClientSecret: "secret"}, true},
		{"missing client id", OAuthConfig{ClientSecret: "secret"}, false},
		{"missing client secret", OAuthConfig{ClientID: "id"}, false},
		{"neither set", OAuthConfig{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.IsConfigured(); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAuthorizeURL_IncludesOfflineAccessAndConsentPrompt(t *testing.T) {
	cfg := OAuthConfig{ClientID: "id", ClientSecret: "secret", RedirectURI: "http://localhost/cb"}
	u := cfg.AuthorizeURL("state-123")
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		t.Fatalf("parse generated URL: %v", err)
	}
	q := req.URL.Query()
	if q.Get("access_type") != "offline" {
		t.Fatalf("expected offline access_type, got %q", q.Get("access_type"))
	}
	if q.Get("prompt") != "consent" {
		t.Fatalf("expected consent prompt, got %q", q.Get("prompt"))
	}
	if q.Get("state") != "state-123" {
		t.Fatalf("expected state to round-trip, got %q", q.Get("state"))
	}
	if q.Get("scope") != scope {
		t.Fatalf("expected the youtube.force-ssl scope, got %q", q.Get("scope"))
	}
}
