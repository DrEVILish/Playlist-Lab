package adapters

import (
	"context"
	"testing"
)

type fakeOAuthTarget struct{ id string }

func (f fakeOAuthTarget) Meta() ServiceMeta { return ServiceMeta{ID: f.id} }
func (f fakeOAuthTarget) SearchCatalog(ctx context.Context, query string, userID int64, allowLive, allowStatic bool) ([]MatchResult, error) {
	return nil, nil
}
func (f fakeOAuthTarget) MatchTracks(ctx context.Context, tracks []TrackInfo, cfg TargetConfig, userID int64, progress func(current, total int), isCancelled func() bool) ([]MatchResult, error) {
	return nil, nil
}
func (f fakeOAuthTarget) CreatePlaylist(ctx context.Context, name string, matches []MatchResult, cfg TargetConfig, userID int64) (string, string, int, error) {
	return "", "", 0, nil
}
func (f fakeOAuthTarget) IsConfigured() bool { return true }
func (f fakeOAuthTarget) GetOAuthURL(ctx context.Context, userID int64, redirectURI string) (string, error) {
	return "https://example.com/oauth", nil
}
func (f fakeOAuthTarget) HandleOAuthCallback(ctx context.Context, code string, userID int64, redirectURI string) error {
	return nil
}
func (f fakeOAuthTarget) HasValidConnection(ctx context.Context, userID int64) (bool, error) {
	return true, nil
}
func (f fakeOAuthTarget) RevokeConnection(ctx context.Context, userID int64) error { return nil }

// fakePlainTarget deliberately does NOT implement OAuthCapable (e.g. Plex).
type fakePlainTarget struct{ id string }

func (f fakePlainTarget) Meta() ServiceMeta { return ServiceMeta{ID: f.id} }
func (f fakePlainTarget) SearchCatalog(ctx context.Context, query string, userID int64, allowLive, allowStatic bool) ([]MatchResult, error) {
	return nil, nil
}
func (f fakePlainTarget) MatchTracks(ctx context.Context, tracks []TrackInfo, cfg TargetConfig, userID int64, progress func(current, total int), isCancelled func() bool) ([]MatchResult, error) {
	return nil, nil
}
func (f fakePlainTarget) CreatePlaylist(ctx context.Context, name string, matches []MatchResult, cfg TargetConfig, userID int64) (string, string, int, error) {
	return "", "", 0, nil
}
func (f fakePlainTarget) IsConfigured() bool { return true }

func TestRegistry_OAuthCapablePreservedThroughWrapper(t *testing.T) {
	r := NewRegistry()
	r.RegisterTarget(fakeOAuthTarget{id: "fake-oauth"})

	wrapped, ok := r.GetTarget("fake-oauth")
	if !ok {
		t.Fatal("expected the adapter to be registered")
	}
	oauth, ok := wrapped.(OAuthCapable)
	if !ok {
		t.Fatal("expected the wrapped adapter to still satisfy OAuthCapable - the rate-limiting wrapper must not drop it")
	}
	url, err := oauth.GetOAuthURL(context.Background(), 1, "")
	if err != nil || url != "https://example.com/oauth" {
		t.Fatalf("expected GetOAuthURL to forward to the underlying adapter, got %q, %v", url, err)
	}
}

func TestRegistry_NonOAuthTargetNotFalselyOAuthCapable(t *testing.T) {
	r := NewRegistry()
	r.RegisterTarget(fakePlainTarget{id: "fake-plain"})

	wrapped, ok := r.GetTarget("fake-plain")
	if !ok {
		t.Fatal("expected the adapter to be registered")
	}
	if _, ok := wrapped.(OAuthCapable); ok {
		t.Fatal("a target that isn't OAuthCapable must not become OAuthCapable after wrapping")
	}
}
