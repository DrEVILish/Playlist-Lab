// Package amazon ports adapters/amazon-target.ts: Amazon Music has no
// public API for playlist creation, so this is a stub target that reports
// that limitation - a playlist can be imported FROM Amazon Music (a
// separate, unrelated source adapter) but never exported TO it. Kept as a
// real adapter (not simply omitted) so it still appears in the target list
// with an explanatory error rather than not existing at all.
package amazon

import (
	"context"
	"fmt"

	"github.com/drevilish/playlist-lab/internal/adapters"
)

var errNoPublicAPI = fmt.Errorf("Amazon Music does not provide a public API for playlist creation. You can import FROM Amazon Music but not TO it")

type Target struct{}

func NewTarget() *Target { return &Target{} }

func (t *Target) Meta() adapters.ServiceMeta {
	return adapters.ServiceMeta{ID: "amazon", Name: "Amazon Music", Icon: "amazon", RequiresOAuth: true}
}

func (t *Target) IsConfigured() bool { return false }

func (t *Target) SearchCatalog(ctx context.Context, query string, userID int64, allowLive, allowStatic bool) ([]adapters.MatchResult, error) {
	return nil, errNoPublicAPI
}

func (t *Target) MatchTracks(ctx context.Context, tracks []adapters.TrackInfo, cfg adapters.TargetConfig, userID int64, progress func(current, total int), isCancelled func() bool) ([]adapters.MatchResult, error) {
	return nil, errNoPublicAPI
}

func (t *Target) CreatePlaylist(ctx context.Context, name string, matches []adapters.MatchResult, cfg adapters.TargetConfig, userID int64) (string, string, int, error) {
	return "", "", 0, errNoPublicAPI
}

func (t *Target) GetOAuthURL(ctx context.Context, userID int64, redirectURI string) (string, error) {
	return "", fmt.Errorf("Amazon Music does not support importing playlists via this app. Amazon Music has no public write API")
}

func (t *Target) HandleOAuthCallback(ctx context.Context, code string, userID int64, redirectURI string) error {
	return fmt.Errorf("Amazon Music does not support OAuth for playlist creation")
}

func (t *Target) HasValidConnection(ctx context.Context, userID int64) (bool, error) {
	return false, nil
}

func (t *Target) RevokeConnection(ctx context.Context, userID int64) error { return nil }
