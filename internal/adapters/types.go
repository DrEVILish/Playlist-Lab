// Package adapters ports adapters/types.ts + registry.ts to Go: a common
// interface every source/target music-service integration implements, plus
// a registry that wraps non-Plex adapters' bulk I/O in the app-wide
// external-service concurrency limiter.
package adapters

import "context"

type TrackInfo struct {
	Title  string
	Artist string
	Album  string
}

type PlaylistInfo struct {
	ID         string
	Name       string
	TrackCount int
	DurationMs int64
	CoverURL   string
}

type MatchResult struct {
	SourceTrack      TrackInfo
	TargetTrackID    string
	TargetTitle      string
	TargetArtist     string
	TargetAlbum      string
	TargetResolution string
	IsStaticImage    bool
	Confidence       float64
	Matched          bool
	Skipped          bool
}

type ServiceMeta struct {
	ID            string
	Name          string
	Icon          string
	IsSourceOnly  bool
	RequiresOAuth bool
}

type TargetConfig struct {
	ServerURL   string
	LibraryID   string
	PlexToken   string
	AccessToken string
}

// SourceAdapter is a platform a playlist can be imported from. TS's
// optional (`?`) methods (listPlaylists, searchPlaylists, the
// *Unauthenticated variants) become the small additional interfaces below,
// checked via type assertion - the idiomatic Go replacement for "this
// method may or may not exist" that avoids one bloated interface every
// adapter must stub.
type SourceAdapter interface {
	Meta() ServiceMeta
	FetchTracks(ctx context.Context, playlistURLOrID string, userID int64) (PlaylistInfo, []TrackInfo, error)
}

type PlaylistLister interface {
	ListPlaylists(ctx context.Context, userID int64) ([]PlaylistInfo, error)
}

type PlaylistSearcher interface {
	SearchPlaylists(ctx context.Context, query string, userID int64) ([]PlaylistInfo, error)
}

type UnauthenticatedSource interface {
	SearchPlaylistsUnauthenticated(ctx context.Context, query string) ([]PlaylistInfo, error)
	FetchUserPlaylistsUnauthenticated(ctx context.Context, userID string) ([]PlaylistInfo, error)
	FetchTracksUnauthenticated(ctx context.Context, playlistID string) (PlaylistInfo, []TrackInfo, error)
}

// TargetAdapter is a platform a playlist can be exported/matched to.
type TargetAdapter interface {
	Meta() ServiceMeta
	SearchCatalog(ctx context.Context, query string, userID int64, allowLive, allowStatic bool) ([]MatchResult, error)
	MatchTracks(ctx context.Context, tracks []TrackInfo, cfg TargetConfig, userID int64, progress func(current, total int), isCancelled func() bool) ([]MatchResult, error)
	CreatePlaylist(ctx context.Context, name string, matches []MatchResult, cfg TargetConfig, userID int64) (playlistID, playlistName string, trackCount int, err error)
	IsConfigured() bool
}

// OAuthCapable is implemented by target adapters that authenticate via
// OAuth (most non-Plex targets).
type OAuthCapable interface {
	GetOAuthURL(ctx context.Context, userID int64, redirectURI string) (string, error)
	HandleOAuthCallback(ctx context.Context, code string, userID int64, redirectURI string) error
	HasValidConnection(ctx context.Context, userID int64) (bool, error)
	RevokeConnection(ctx context.Context, userID int64) error
}
