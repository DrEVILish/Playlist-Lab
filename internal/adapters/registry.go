package adapters

import (
	"context"

	"github.com/drevilish/playlist-lab/internal/services/limiter"
)

// Registry holds every registered source/target adapter, keyed by
// ServiceMeta.ID.
type Registry struct {
	sources map[string]SourceAdapter
	targets map[string]TargetAdapter
}

func NewRegistry() *Registry {
	return &Registry{sources: map[string]SourceAdapter{}, targets: map[string]TargetAdapter{}}
}

// RegisterSource wraps a non-Plex source's bulk methods with the app-wide
// externalLimiter (see the limiter package) before storing it - Plex is
// excluded since PlexClient already gates every Plex HTTP call through its
// own limiter, and matchPlaylist() already gates matching through the
// matching limiter, so wrapping the Plex adapter here too would have it
// acquire externalLimiter and then, from inside that same call, block
// forever waiting on a limiter slot it's already holding.
func (r *Registry) RegisterSource(a SourceAdapter) {
	if a.Meta().ID != "plex" {
		a = rateLimitedSource{a}
	}
	r.sources[a.Meta().ID] = a
}

func (r *Registry) RegisterTarget(a TargetAdapter) {
	if a.Meta().ID != "plex" {
		base := rateLimitedTarget{TargetAdapter: a}
		// A wrapper that embeds TargetAdapter alone would silently drop
		// OAuthCapable even though the underlying concrete adapter
		// implements it - Go only promotes an embedded interface's own
		// method set, it doesn't forward the rest of the dynamic value's
		// methods. rateLimitedOAuthTarget exists so a.(OAuthCapable) on the
		// wrapped value only succeeds when the original adapter actually
		// was OAuthCapable, never unconditionally.
		if oauth, ok := a.(OAuthCapable); ok {
			a = rateLimitedOAuthTarget{rateLimitedTarget: base, oauth: oauth}
		} else {
			a = base
		}
	}
	r.targets[a.Meta().ID] = a
}

func (r *Registry) GetSource(id string) (SourceAdapter, bool) {
	a, ok := r.sources[id]
	return a, ok
}

func (r *Registry) GetTarget(id string) (TargetAdapter, bool) {
	a, ok := r.targets[id]
	return a, ok
}

func (r *Registry) ListSources() []SourceAdapter {
	out := make([]SourceAdapter, 0, len(r.sources))
	for _, a := range r.sources {
		out = append(out, a)
	}
	return out
}

func (r *Registry) ListTargets() []TargetAdapter {
	out := make([]TargetAdapter, 0, len(r.targets))
	for _, a := range r.targets {
		if !a.Meta().IsSourceOnly {
			out = append(out, a)
		}
	}
	return out
}

// rateLimitedSource/rateLimitedTarget wrap only the bulk network-I/O calls
// (fetching/searching/writing a whole playlist) with the external-service
// concurrency limiter - one-shot OAuth/config calls are left unlimited,
// matching registry.ts's SOURCE_METHODS/TARGET_METHODS split. Go has no
// runtime way to wrap "some of an interface's methods" generically the way
// the TS version's Object.create() trick does, so this is a plain
// decorator implementing the same interface, forwarding every method and
// adding the acquire/release around the ones that matter.
type rateLimitedSource struct{ SourceAdapter }

func (s rateLimitedSource) FetchTracks(ctx context.Context, playlistURLOrID string, userID int64) (PlaylistInfo, []TrackInfo, error) {
	limiter.External.Acquire()
	defer limiter.External.Release()
	return s.SourceAdapter.FetchTracks(ctx, playlistURLOrID, userID)
}

type rateLimitedTarget struct{ TargetAdapter }

// rateLimitedOAuthTarget is used instead of rateLimitedTarget only when the
// wrapped adapter implements OAuthCapable, so the wrapped value's own type
// assertions stay truthful (see RegisterTarget).
type rateLimitedOAuthTarget struct {
	rateLimitedTarget
	oauth OAuthCapable
}

func (t rateLimitedOAuthTarget) GetOAuthURL(ctx context.Context, userID int64, redirectURI string) (string, error) {
	return t.oauth.GetOAuthURL(ctx, userID, redirectURI)
}

func (t rateLimitedOAuthTarget) HandleOAuthCallback(ctx context.Context, code string, userID int64, redirectURI string) error {
	return t.oauth.HandleOAuthCallback(ctx, code, userID, redirectURI)
}

func (t rateLimitedOAuthTarget) HasValidConnection(ctx context.Context, userID int64) (bool, error) {
	return t.oauth.HasValidConnection(ctx, userID)
}

func (t rateLimitedOAuthTarget) RevokeConnection(ctx context.Context, userID int64) error {
	return t.oauth.RevokeConnection(ctx, userID)
}

func (t rateLimitedTarget) SearchCatalog(ctx context.Context, query string, userID int64, allowLive, allowStatic bool) ([]MatchResult, error) {
	return limiter.Run(limiter.External, func() ([]MatchResult, error) {
		return t.TargetAdapter.SearchCatalog(ctx, query, userID, allowLive, allowStatic)
	})
}

func (t rateLimitedTarget) MatchTracks(ctx context.Context, tracks []TrackInfo, cfg TargetConfig, userID int64, progress func(current, total int), isCancelled func() bool) ([]MatchResult, error) {
	return limiter.Run(limiter.External, func() ([]MatchResult, error) {
		return t.TargetAdapter.MatchTracks(ctx, tracks, cfg, userID, progress, isCancelled)
	})
}

func (t rateLimitedTarget) CreatePlaylist(ctx context.Context, name string, matches []MatchResult, cfg TargetConfig, userID int64) (string, string, int, error) {
	limiter.External.Acquire()
	defer limiter.External.Release()
	return t.TargetAdapter.CreatePlaylist(ctx, name, matches, cfg, userID)
}
