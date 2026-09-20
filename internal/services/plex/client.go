// Package plex ports services/plex.ts's PlexClient/PlexService to Go: HTTP
// access to a user's Plex Media Server for library, playlist, and track
// operations. Only the subset used by Phase 1 (playlist CRUD) is ported so
// far - the original file also covers search, history, sonic similarity,
// sharing, etc., which land in later phases alongside the routes that use
// them.
package plex

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/drevilish/playlist-lab/internal/services/limiter"
)

// AuthError is thrown when Plex rejects the stored auth token (revoked,
// expired, or otherwise invalid), so route handlers can answer 401
// (prompting re-login) instead of a generic 500.
type AuthError struct{ Message string }

func (e *AuthError) Error() string { return e.Message }

// UnreachableError means the server was already known to be down and the
// call fast-failed instead of waiting on a fresh timeout.
type UnreachableError struct{ Message string }

func (e *UnreachableError) Error() string { return e.Message }

// ResolveToken picks the right token to authenticate directly against a
// user's selected Plex Media Server: servers a user owns accept their
// plex.tv account token, but servers merely shared with them require the
// server-specific access token from /api/resources instead - see plex.ts's
// resolvePlexToken for the full rationale (using the wrong one sends
// shared-server users into an unbreakable re-login loop).
func ResolveToken(userToken string, serverAccessToken string) string {
	if serverAccessToken != "" {
		return serverAccessToken
	}
	return userToken
}

type Library struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type Folder struct {
	Path       string `json:"path"`
	Accessible bool   `json:"accessible"`
}

type Playlist struct {
	RatingKey    string `json:"ratingKey"`
	Title        string `json:"title"`
	PlaylistType string `json:"playlistType"`
	Smart        bool   `json:"smart"`
	Composite    string `json:"composite"`
	LeafCount    int    `json:"leafCount"`
	Duration     int64  `json:"duration"`
	AddedAt      int64  `json:"addedAt"`
	UpdatedAt    int64  `json:"updatedAt"`
}

type media struct {
	AudioCodec      string `json:"audioCodec"`
	Bitrate         int    `json:"bitrate"`
	AudioSampleRate int    `json:"audioSampleRate"`
	Part            []part `json:"Part"`
}

// part is a track's on-disk file location, used by playlist export
// (export.go) to write real file paths into M3U/PLS/XSPF output - the
// reason a whole Media/Part struct exists on Track beyond just the codec
// Codec() reads.
type part struct {
	File string `json:"file"`
}

// FilePath returns this track's first Media/Part's on-disk path, or "" if
// Plex has none (e.g. a track whose file was removed from disk but not yet
// rescanned out of the library).
func (t Track) FilePath() string {
	if len(t.Media) == 0 || len(t.Media[0].Part) == 0 {
		return ""
	}
	return t.Media[0].Part[0].File
}

type Track struct {
	RatingKey string `json:"ratingKey"`
	// Plex returns this as a JSON number for a real playlist item, and omits
	// it entirely for a smart (dynamically-generated) playlist's items -
	// int rather than string because a plain string field fails to
	// unmarshal a numeric JSON value.
	PlaylistItemID       int     `json:"playlistItemID"`
	Title                string  `json:"title"`
	OriginalTitle        string  `json:"originalTitle"`
	GrandparentKey       string  `json:"grandparentKey"`
	GrandparentRatingKey string  `json:"grandparentRatingKey"`
	ParentTitle          string  `json:"parentTitle"`
	GrandparentTitle     string  `json:"grandparentTitle"`
	Duration             int64   `json:"duration"`
	LibrarySectionID     int     `json:"librarySectionID"`
	Year                 int     `json:"year"`
	ParentYear           int     `json:"parentYear"`
	Key                  string  `json:"key"`
	Media                []media `json:"Media"`

	// Fields used by the discovery/mix-seeding queries (getRecentTracks,
	// getStalePlayedTracks, getTracksWithAdvancedFilters, etc. - see
	// discovery.go) - not needed by the plain playlist CRUD paths above,
	// which is why they weren't on this struct originally.
	LastViewedAt  int64          `json:"lastViewedAt"`
	ViewCount     int            `json:"viewCount"`
	SkipCount     int            `json:"skipCount"`
	AddedAt       int64          `json:"addedAt"`
	Index         int            `json:"index"`
	ParentIndex   int            `json:"parentIndex"`
	UserRating    float64        `json:"userRating"`
	ParentStudio  string         `json:"parentStudio"`
	RatingCount   int            `json:"ratingCount"`
	Genre         []Tag          `json:"Genre"`
	Mood          []Tag          `json:"Mood"`
	Style         []Tag          `json:"Style"`
	Collection    []Tag          `json:"Collection"`
	MusicAnalysis *MusicAnalysis `json:"musicAnalysis,omitempty"`

	// Movie/show fields, added for library-wide Collection filtering
	// (librarysearch.go, DESIGN.md §11.11) - unused by the track-only paths
	// above, same as the discovery.go block below it.
	Studio        string `json:"studio"` // movie studio, or show network (Plex has no separate network field)
	ContentRating string `json:"contentRating"`
	Role          []Tag  `json:"Role"`     // cast/actor tags, same shape as Genre/Mood/Style
	Director      []Tag  `json:"Director"` // director credit tags, same shape as Role
	Writer        []Tag  `json:"Writer"`   // writer credit tags, same shape as Role

	// PrimaryGuid is Plex's own internal identity guid (e.g.
	// "plex://movie/..."), always present as a plain string - declared
	// explicitly so Go's json decoder doesn't case-fold the "guid" key onto
	// the Guid field below when an item's response has no "Guid" array at
	// all (confirmed live: some library items - e.g. extras/clips with no
	// external agent match - omit "Guid" entirely even with
	// includeGuids=1, and without this field encoding/json's case-
	// insensitive fallback matching tried to unmarshal that item's plain
	// "guid" string into the []ExternalGuid slice below and errored).
	PrimaryGuid string `json:"guid"`

	// Guid carries an item's external agent ids (tmdb://, imdb://, tvdb://) -
	// Plex omits this by default and only includes it when fetched with
	// includeGuids=1 (see GetLibraryItemsWithGuids). Confirmed live that
	// Plex's own guid= search filter only matches its internal
	// plex://... guid, not these external ids, so Collections' external-
	// list builder (DESIGN.md §11.11) matches against this field
	// client-side instead.
	Guid []ExternalGuid `json:"Guid,omitempty"`
}

type ExternalGuid struct {
	ID string `json:"id"`
}

// Tag is Plex's shape for a Genre/Mood/Style/Collection tag entry.
type Tag struct {
	Tag string `json:"tag"`
}

// MusicAnalysis is Plex's sonic-analysis payload, present on a track when
// its "nearest" (sonic similarity) data has been computed.
type MusicAnalysis struct {
	Tempo        float64 `json:"tempo"`
	Energy       float64 `json:"energy"`
	Danceability float64 `json:"danceability"`
}

// DisplayArtist prefers the track-level artist (originalTitle) over the
// album/grandparent artist whenever they differ - the album artist is just
// the library's folder/grouping artist and can be wrong for compilations,
// soundtracks, and "Various Artists" albums.
func (t Track) DisplayArtist() string {
	if t.OriginalTitle != "" && !strings.EqualFold(t.OriginalTitle, t.GrandparentTitle) {
		return t.OriginalTitle
	}
	return t.GrandparentTitle
}

func (t Track) Codec() string {
	if len(t.Media) == 0 {
		return ""
	}
	return strings.ToUpper(t.Media[0].AudioCodec)
}

type mediaContainer struct {
	MediaContainer struct {
		Directory []struct {
			Key      string `json:"key"`
			Title    string `json:"title"`
			Type     string `json:"type"`
			Location []struct {
				Path string `json:"path"`
			} `json:"Location"`
		} `json:"Directory"`
		Metadata  []json.RawMessage `json:"Metadata"`
		Hub       []Hub             `json:"Hub"`
		TotalSize int               `json:"totalSize"`
	} `json:"MediaContainer"`
}

// Hub is one of Plex's "Related Hubs" / search-hub / popular-tracks
// containers - a titled, typed group of Metadata items (e.g. "Popular",
// "Fans Also Like", the track/album hubs from /hubs/search).
type Hub struct {
	Type     string            `json:"type"`
	Title    string            `json:"title"`
	Metadata []json.RawMessage `json:"Metadata"`
}

type Client struct {
	ServerURL string
	Token     string
	ClientID  string
	Product   string
	http      *http.Client

	// Per-instance search caches, matching plex.ts's design: a PlexClient is
	// constructed once per matching run (see matching.go) and reused across
	// every track in that run, so an artist looked up for one track is
	// already warm for the next track by the same artist. Not shared across
	// requests/instances - a fresh matching run gets a fresh cache, same as
	// the Node server.
	cacheMu           sync.Mutex
	searchCache       map[string]cacheEntry[[]Track]
	artistLookupCache map[string]cacheEntry[[]Track]
	artistTracksCache map[string]cacheEntry[[]Track]
}

// FetchMedia streams a Plex-relative media path (a playlist's Composite
// thumb, cover art) from this client's server. Callers relay the bytes to
// the browser themselves - see handlers.ProxyHandler - so that a token is
// never handed to the browser, and so that cover art keeps working for
// clients with no route to the Plex server. The caller closes the returned
// body.
//
// This replaced a MediaURL helper that built a directly-loadable
// ?X-Plex-Token=... URL for templates to embed. That put the user's token in
// the page (hundreds of times over, once per thumb) and only rendered at all
// for browsers that could reach the Plex server themselves; it is gone
// rather than deprecated so nothing reintroduces it.
func (c *Client) FetchMedia(path string) (io.ReadCloser, string, error) {
	if path == "" {
		return nil, "", fmt.Errorf("empty media path")
	}
	req, err := http.NewRequest(http.MethodGet, c.ServerURL+path, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, "", fmt.Errorf("plex returned %s for %s", resp.Status, path)
	}
	return resp.Body, resp.Header.Get("Content-Type"), nil
}

type cacheEntry[T any] struct {
	value     T
	expiresAt time.Time
}

const (
	searchCacheTTL   = 5 * time.Minute
	maxCacheEntries  = 500
	maxArtistCatalog = 3000
)

func NewClient(serverURL, token, clientID, product string) *Client {
	serverURL = strings.TrimSuffix(serverURL, "/")
	transport := http.DefaultTransport
	if strings.HasPrefix(serverURL, "https://") {
		// Relaxed TLS for direct-IP/self-signed Plex connections, matching
		// the Node client's httpsAgent({ rejectUnauthorized: false }).
		transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec
	}
	return &Client{
		ServerURL:         serverURL,
		Token:             token,
		ClientID:          clientID,
		Product:           product,
		http:              &http.Client{Timeout: 60 * time.Second, Transport: transport},
		searchCache:       map[string]cacheEntry[[]Track]{},
		artistLookupCache: map[string]cacheEntry[[]Track]{},
		artistTracksCache: map[string]cacheEntry[[]Track]{},
	}
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Token", c.Token)
	req.Header.Set("X-Plex-Product", c.Product)
	req.Header.Set("X-Plex-Client-Identifier", c.ClientID)
	req.Header.Set("X-Plex-Platform", "Node.js")
	if req.Header.Get("X-Plex-Container-Size") == "" {
		req.Header.Set("X-Plex-Container-Size", "50")
	}

	if down, msg := checkUnreachable(c.ServerURL); down {
		return nil, &UnreachableError{Message: msg}
	}

	limiter.Plex.Acquire()
	defer limiter.Plex.Release()

	resp, err := c.http.Do(req)
	if err != nil {
		markConnectionFailure(c.ServerURL, err)
		return nil, fmt.Errorf("Plex server is unreachable")
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		return nil, &AuthError{Message: "Invalid or expired Plex token"}
	}
	clearUnreachable(c.ServerURL)
	return resp, nil
}

func (c *Client) get(path string) (*mediaContainer, error) {
	req, err := http.NewRequest(http.MethodGet, c.ServerURL+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("not found")
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Plex request failed: status %d", resp.StatusCode)
	}
	var mc mediaContainer
	if err := json.NewDecoder(resp.Body).Decode(&mc); err != nil {
		return nil, err
	}
	return &mc, nil
}

// getWithHeaders is get() plus caller-supplied header overrides (e.g.
// X-Plex-Container-Size, which do() defaults to 50 - a header, not a query
// param, wins over a param of the same name, so this is the only way to ask
// for more than 50 results). do() only fills in a header if the request
// doesn't already have one, so setting it here first makes it stick.
func (c *Client) getWithHeaders(path string, headers map[string]string) (*mediaContainer, error) {
	req, err := http.NewRequest(http.MethodGet, c.ServerURL+path, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Plex request failed: status %d", resp.StatusCode)
	}
	var mc mediaContainer
	if err := json.NewDecoder(resp.Body).Decode(&mc); err != nil {
		return nil, err
	}
	return &mc, nil
}

func (c *Client) mutate(method, path string) (*mediaContainer, int, error) {
	req, err := http.NewRequest(method, c.ServerURL+path, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, resp.StatusCode, fmt.Errorf("Plex request failed: status %d", resp.StatusCode)
	}
	var mc mediaContainer
	_ = json.NewDecoder(resp.Body).Decode(&mc) // many mutations return an empty body
	return &mc, resp.StatusCode, nil
}

func (c *Client) GetLibraries() ([]Library, error) {
	mc, err := c.get("/library/sections")
	if err != nil {
		return nil, err
	}
	libs := make([]Library, 0, len(mc.MediaContainer.Directory))
	for _, d := range mc.MediaContainer.Directory {
		libs = append(libs, Library{ID: d.Key, Name: d.Title, Type: d.Type})
	}
	return libs, nil
}

func (c *Client) GetLibraryFolders(libraryID string) ([]Folder, error) {
	mc, err := c.get("/library/sections/" + url.PathEscape(libraryID))
	if err != nil {
		return nil, err
	}
	if len(mc.MediaContainer.Directory) == 0 {
		return nil, nil
	}
	folders := make([]Folder, 0, len(mc.MediaContainer.Directory[0].Location))
	for _, loc := range mc.MediaContainer.Directory[0].Location {
		folders = append(folders, Folder{Path: loc.Path, Accessible: true})
	}
	return folders, nil
}

func (c *Client) ScanLibrary(libraryID, path string) error {
	p := "/library/sections/" + url.PathEscape(libraryID) + "/refresh"
	if path != "" {
		p += "?path=" + url.QueryEscape(path)
	}
	_, _, err := c.mutate(http.MethodGet, p)
	return err
}

func (c *Client) GetPlaylists() ([]Playlist, error) {
	mc, err := c.get("/playlists")
	if err != nil {
		return nil, err
	}
	return decodePlaylists(mc.MediaContainer.Metadata)
}

func (c *Client) CreatePlaylist(name, libraryURI string, trackURIs []string) (*Playlist, error) {
	p := fmt.Sprintf("/playlists?type=audio&title=%s&smart=0&uri=%s", url.QueryEscape(name), url.QueryEscape(libraryURI))
	mc, _, err := c.mutate(http.MethodPost, p)
	if err != nil {
		return nil, err
	}
	playlists, err := decodePlaylists(mc.MediaContainer.Metadata)
	if err != nil || len(playlists) == 0 {
		return nil, fmt.Errorf("failed to create playlist - no playlist returned")
	}
	if len(trackURIs) > 0 {
		if err := c.AddToPlaylist(playlists[0].RatingKey, trackURIs); err != nil {
			return nil, err
		}
	}
	return &playlists[0], nil
}

func (c *Client) GetPlaylistTracks(playlistID string) ([]Track, error) {
	mc, err := c.get("/playlists/" + url.PathEscape(playlistID) + "/items")
	if err != nil {
		return nil, err
	}
	tracks := make([]Track, 0, len(mc.MediaContainer.Metadata))
	for _, raw := range mc.MediaContainer.Metadata {
		var t Track
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, err
		}
		tracks = append(tracks, t)
	}
	return tracks, nil
}

func (c *Client) RenamePlaylist(playlistID, title string) error {
	_, _, err := c.mutate(http.MethodPut, "/playlists/"+url.PathEscape(playlistID)+"?title="+url.QueryEscape(title))
	return err
}

// batchRatingKeyURIs groups itemURIs into batches of up to 50, each
// collapsed into a single comma-joined-ratingKeys URI sharing one server://
// prefix - matching python-plexapi's approach of one request per batch
// rather than one per item. Shared by AddToPlaylist and the Collection
// mutations in collections.go.
func batchRatingKeyURIs(itemURIs []string) ([]string, error) {
	const batchSize = 50
	if len(itemURIs) == 0 {
		return nil, nil
	}
	prefixIdx := strings.Index(itemURIs[0], "/library/metadata/")
	if prefixIdx == -1 {
		return nil, fmt.Errorf("invalid item URI: %s", itemURIs[0])
	}
	uriPrefix := itemURIs[0][:prefixIdx]

	var batches []string
	for i := 0; i < len(itemURIs); i += batchSize {
		end := min(i+batchSize, len(itemURIs))
		batch := itemURIs[i:end]
		ratingKeys := make([]string, len(batch))
		for j, uri := range batch {
			parts := strings.SplitN(uri, "/library/metadata/", 2)
			if len(parts) != 2 {
				return nil, fmt.Errorf("invalid item URI: %s", uri)
			}
			ratingKeys[j] = parts[1]
		}
		batches = append(batches, uriPrefix+"/library/metadata/"+strings.Join(ratingKeys, ","))
	}
	return batches, nil
}

// AddToPlaylist adds tracks in batches of 50, matching python-plexapi's
// approach of a single comma-joined ratingKeys URI per batch rather than
// one request per track.
func (c *Client) AddToPlaylist(playlistID string, trackURIs []string) error {
	batches, err := batchRatingKeyURIs(trackURIs)
	if err != nil {
		return err
	}
	for _, batchURI := range batches {
		p := "/playlists/" + url.PathEscape(playlistID) + "/items?uri=" + url.QueryEscape(batchURI)
		if _, _, err := c.mutate(http.MethodPut, p); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) RemoveFromPlaylist(playlistID, playlistItemID string) error {
	_, status, err := c.mutate(http.MethodDelete, "/playlists/"+url.PathEscape(playlistID)+"/items/"+url.PathEscape(playlistItemID))
	if status == http.StatusNotFound {
		return fmt.Errorf("Playlist or item not found")
	}
	return err
}

// RemoveMultipleFromPlaylist removes several items in one pass. Plex
// reassigns the remaining items' playlistItemID after each removal, so a
// 404 partway through almost always just means Plex already shifted that
// item out from under the ID collected up front - skipped rather than
// treated as a real failure, matching plex.ts's removeMultipleFromPlaylist.
func (c *Client) RemoveMultipleFromPlaylist(playlistID string, playlistItemIDs []string) error {
	for _, id := range playlistItemIDs {
		if err := c.RemoveFromPlaylist(playlistID, id); err != nil {
			if err.Error() == "Playlist or item not found" {
				continue
			}
			return err
		}
	}
	return nil
}

func (c *Client) MovePlaylistItem(playlistID, playlistItemID, afterItemID string) error {
	p := "/playlists/" + url.PathEscape(playlistID) + "/items/" + url.PathEscape(playlistItemID) + "/move?after=" + url.QueryEscape(afterItemID)
	_, _, err := c.mutate(http.MethodPut, p)
	return err
}

func (c *Client) DeletePlaylist(playlistID string) error {
	_, _, err := c.mutate(http.MethodDelete, "/playlists/"+url.PathEscape(playlistID))
	return err
}

// UploadPlaylistPoster downloads imageURL and uploads it as a playlist's
// poster, ports plex.ts's uploadPlaylistPoster. Non-fatal by convention -
// callers log and continue on error, the playlist still works without one.
func (c *Client) UploadPlaylistPoster(playlistID, imageURL string) error {
	imgReq, err := http.NewRequest(http.MethodGet, imageURL, nil)
	if err != nil {
		return err
	}
	imgReq.Header.Set("User-Agent", "Playlist Lab/1.0")
	imgResp, err := c.http.Do(imgReq)
	if err != nil {
		return err
	}
	defer imgResp.Body.Close()
	if imgResp.StatusCode >= 400 {
		return fmt.Errorf("failed to download poster image: status %d", imgResp.StatusCode)
	}
	body, err := io.ReadAll(imgResp.Body)
	if err != nil {
		return err
	}
	contentType := imgResp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/jpeg"
	}
	return c.UploadPlaylistPosterBytes(playlistID, body, contentType)
}

// UploadPlaylistPosterBytes is UploadPlaylistPoster's shared upload half,
// split out so a directly-uploaded file (playlists.go's cover-upload form,
// which has the bytes and content type already - no image URL to fetch) can
// reuse it instead of round-tripping through a URL fetch that would never
// hit the network.
func (c *Client) UploadPlaylistPosterBytes(playlistID string, body []byte, contentType string) error {
	req, err := http.NewRequest(http.MethodPost, c.ServerURL+"/library/metadata/"+url.PathEscape(playlistID)+"/posters", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("failed to upload poster: status %d", resp.StatusCode)
	}
	return nil
}

// GetTrackDetails fetches a single track's full metadata by ratingKey, used
// to resolve a remembered manual match (or a missing-track's stored
// ratingKey) to fresh details rather than trusting a possibly-stale cache.
// Returns nil, nil (not an error) if the track no longer exists in Plex.
func (c *Client) GetTrackDetails(ratingKey string) (*Track, error) {
	mc, err := c.get("/library/metadata/" + url.PathEscape(ratingKey))
	if err != nil {
		if err.Error() == "not found" {
			return nil, nil
		}
		return nil, err
	}
	if len(mc.MediaContainer.Metadata) == 0 {
		return nil, nil
	}
	var t Track
	if err := json.Unmarshal(mc.MediaContainer.Metadata[0], &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func decodePlaylists(raw []json.RawMessage) ([]Playlist, error) {
	out := make([]Playlist, 0, len(raw))
	for _, r := range raw {
		var p Playlist
		if err := json.Unmarshal(r, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// BuildTrackURI builds a server://<clientId>/.../library/metadata/<ratingKey>
// URI for CreatePlaylist()/AddToPlaylist(), the format every playlist
// mutation uses to reference a track.
func BuildTrackURI(serverClientID, ratingKey string) string {
	return "server://" + serverClientID + "/com.plexapp.plugins.library/library/metadata/" + ratingKey
}

// BuildLibraryURI builds a server://<id>/.../library/sections/<libraryId>
// URI for CreatePlaylist(). id should be the server's machine identifier
// (see GetMachineIdentifier) when available - client.ClientID (this app's
// own identifier, not the Plex server's) is only a fallback.
func (c *Client) BuildLibraryURI(libraryID, machineIdentifier string) string {
	id := machineIdentifier
	if id == "" {
		id = c.ClientID
	}
	return "server://" + id + "/com.plexapp.plugins.library/library/sections/" + libraryID
}

func (c *Client) BuildTrackURI(ratingKey, machineIdentifier string) string {
	id := machineIdentifier
	if id == "" {
		id = c.ClientID
	}
	return BuildTrackURI(id, ratingKey)
}

// GetMachineIdentifier returns the Plex server's own unique identifier,
// used (in preference to this app's client ID) when building server://
// URIs for playlist creation.
func (c *Client) GetMachineIdentifier() (string, error) {
	req, err := http.NewRequest(http.MethodGet, c.ServerURL+"/", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("failed to get machine identifier: status %d", resp.StatusCode)
	}
	var data struct {
		MediaContainer struct {
			MachineIdentifier string `json:"machineIdentifier"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}
	return data.MediaContainer.MachineIdentifier, nil
}
