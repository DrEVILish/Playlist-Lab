// dzclient.go is a direct Go port of deezer-js's GW ("gw-light.php" private
// API) + public API clients, and of deemix's own get_track_url call against
// media.deezer.com - the pieces of the original Node deemix-server this app
// actually used (see deemix.go's package doc). It replaces talking to a
// separate deemix-server process over HTTP with talking to Deezer directly.
package deemix

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

// dzUserAgent matches deezer-js's own default - Deezer's gateway is known to
// behave differently (or reject requests outright) for unrecognized UAs.
const dzUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/79.0.3945.130 Safari/537.36"

var (
	errNotLoggedIn  = errors.New("not logged in to deezer")
	errWrongLicense = errors.New("this deezer account's plan can't stream at the requested quality")
	errGeolocation  = errors.New("this deezer account can't stream from its current country")
	errNoTrackURL   = errors.New("deezer returned no download url for this track")
)

// dzUser is the subset of deezer.getUserData's response this client needs -
// mirrors deezer-js's Deezer.current_user.
type dzUser struct {
	id                string
	licenseToken      string
	canStreamHQ       bool
	canStreamLossless bool
	country           string
}

// gwError is a GW API error response (result_json.error), preserved so
// callers can recognize the "invalid token" case that warrants a retry.
type gwError struct {
	raw string
}

func (e *gwError) Error() string { return "deezer gw error: " + e.raw }

func (e *gwError) isInvalidToken() bool {
	return strings.Contains(e.raw, "invalid api token") || strings.Contains(e.raw, "Invalid CSRF token")
}

// dzClient is a minimal, direct Deezer API client: ARL-cookie login against
// the private "gw-light" API (for track/album metadata and get_url license
// tokens) plus the public api.deezer.com API (for search, which needs no
// auth). One instance is shared for the whole app, matching deemix.go's
// existing one-shared-session model.
type dzClient struct {
	http *http.Client
	jar  *cookiejar.Jar
	arl  string

	// gwLightURL overrides gw-light.php's real URL - test seam only, left
	// empty (meaning "use the real endpoint") in production.
	gwLightURL string

	mu       sync.Mutex
	apiToken string // gw "checkForm" CSRF token
	user     dzUser
	loggedIn bool
}

func newDZClient(arl string) *dzClient {
	jar, _ := cookiejar.New(nil)
	c := &dzClient{http: &http.Client{Timeout: 30 * time.Second, Jar: jar}, jar: jar}
	c.setARL(arl)
	return c
}

// setARL updates the ARL this client logs in with. The arl cookie itself is
// sent as an explicit header on every gw request (see rawGWCall), not
// seeded into the jar - the jar's only job is capturing and replaying
// whatever session cookie (sid) Deezer's server sets back on login, which
// http.Client does automatically (it appends jar cookies onto whatever
// Cookie header a request already carries, rather than replacing it) as
// long as every request actually goes through this client.
func (c *dzClient) setARL(arl string) {
	c.mu.Lock()
	c.arl = arl
	c.apiToken = ""
	c.loggedIn = false
	c.mu.Unlock()
}

func (c *dzClient) currentARL() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.arl
}

// login establishes a fresh gw session: the ARL cookie identifies the
// account, deezer.getUserData both confirms it's valid and hands back the
// CSRF token every other gw call needs plus the license/streaming-quality
// flags get_track_url needs later.
func (c *dzClient) login() error {
	arl := c.currentARL()
	if arl == "" {
		return fmt.Errorf("Deemix ARL is not configured - set DEEMIX_ARL")
	}
	raw, err := c.rawGWCall("deezer.getUserData", nil, "null")
	if err != nil {
		return err
	}
	var data struct {
		CheckForm string `json:"checkForm"`
		USER      struct {
			USER_ID json.Number `json:"USER_ID"`
			OPTIONS struct {
				LicenseToken   string `json:"license_token"`
				WebHQ          bool   `json:"web_hq"`
				MobileHQ       bool   `json:"mobile_hq"`
				WebLossless    bool   `json:"web_lossless"`
				MobileLossless bool   `json:"mobile_lossless"`
				LicenseCountry string `json:"license_country"`
			} `json:"OPTIONS"`
		} `json:"USER"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return fmt.Errorf("parsing deezer user data: %w", err)
	}
	if data.USER.USER_ID == "" || data.USER.USER_ID == "0" {
		return fmt.Errorf("deemix rejected DEEMIX_ARL - it may have expired, get a fresh one from your Deezer account")
	}

	c.mu.Lock()
	c.apiToken = data.CheckForm
	c.user = dzUser{
		id:                data.USER.USER_ID.String(),
		licenseToken:      data.USER.OPTIONS.LicenseToken,
		canStreamHQ:       data.USER.OPTIONS.WebHQ || data.USER.OPTIONS.MobileHQ,
		canStreamLossless: data.USER.OPTIONS.WebLossless || data.USER.OPTIONS.MobileLossless,
		country:           data.USER.OPTIONS.LicenseCountry,
	}
	c.loggedIn = true
	c.mu.Unlock()
	return nil
}

// gwCall runs one gw-light API method, logging in first if this is the
// first call and retrying once (after a fresh login) if the token gw
// rejects turns out to be stale - mirrors deezer-js gw.js's api_call.
func (c *dzClient) gwCall(method string, args map[string]any) (json.RawMessage, error) {
	c.mu.Lock()
	token := c.apiToken
	c.mu.Unlock()
	if token == "" {
		if err := c.login(); err != nil {
			return nil, err
		}
		c.mu.Lock()
		token = c.apiToken
		c.mu.Unlock()
	}

	raw, err := c.rawGWCall(method, args, token)
	var gwErr *gwError
	if errors.As(err, &gwErr) && gwErr.isInvalidToken() {
		if lerr := c.login(); lerr != nil {
			return nil, lerr
		}
		c.mu.Lock()
		token = c.apiToken
		c.mu.Unlock()
		return c.rawGWCall(method, args, token)
	}
	return raw, err
}

func (c *dzClient) rawGWCall(method string, args map[string]any, token string) (json.RawMessage, error) {
	body := []byte("{}")
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			return nil, err
		}
		body = b
	}

	q := url.Values{
		"api_version": {"1.0"},
		"api_token":   {token},
		"input":       {"3"},
		"method":      {method},
	}
	endpoint := c.gwLightURL
	if endpoint == "" {
		endpoint = "https://www.deezer.com/ajax/gw-light.php"
	}
	req, err := http.NewRequest(http.MethodPost, endpoint+"?"+q.Encode(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", dzUserAgent)
	req.Header.Set("Cookie", "arl="+c.currentARL())
	// c.http's cookiejar appends whatever session cookie (sid) Deezer's
	// server set back on login's response on top of the arl cookie above -
	// see setARL's doc comment.

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var data struct {
		Error   json.RawMessage `json:"error"`
		Results json.RawMessage `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decoding gw response for %s: %w", method, err)
	}
	if errStr := string(bytes.TrimSpace(data.Error)); errStr != "" && errStr != "{}" && errStr != "[]" && errStr != "null" {
		return nil, &gwError{raw: errStr}
	}
	return data.Results, nil
}

// gwTrack is song.getData's response shape, trimmed to the fields the
// download pipeline and tagger actually use. Field names/shapes below are
// verified against the real API (see this package's git history for the
// live calls used to confirm them), not just the old JS source - a couple
// (SNG_CONTRIBUTORS's role keys, GAIN as a string) differ subtly from what
// deemix's own JS types implied.
type gwTrack struct {
	SNG_ID                json.Number `json:"SNG_ID"`
	SNG_TITLE             string      `json:"SNG_TITLE"`
	DURATION              json.Number `json:"DURATION"`
	MD5_ORIGIN            string      `json:"MD5_ORIGIN"`
	MEDIA_VERSION         json.Number `json:"MEDIA_VERSION"`
	ALB_ID                json.Number `json:"ALB_ID"`
	ALB_TITLE             string      `json:"ALB_TITLE"`
	ALB_PICTURE           string      `json:"ALB_PICTURE"`
	ART_ID                json.Number `json:"ART_ID"`
	ART_NAME              string      `json:"ART_NAME"`
	TRACK_TOKEN           string      `json:"TRACK_TOKEN"`
	TRACK_NUMBER          json.Number `json:"TRACK_NUMBER"`
	DISK_NUMBER           json.Number `json:"DISK_NUMBER"`
	PHYSICAL_RELEASE_DATE string      `json:"PHYSICAL_RELEASE_DATE"`
	EXPLICIT_LYRICS       json.Number `json:"EXPLICIT_LYRICS"`
	ISRC                  string      `json:"ISRC"`
	GAIN                  string      `json:"GAIN"`
	RANK                  json.Number `json:"RANK"`
	LYRICS_ID             json.Number `json:"LYRICS_ID"`
	// SNG_CONTRIBUTORS is role -> credited names, e.g.
	// {"main_artist":["Daft Punk"],"author":["..."],"composer":["..."]}.
	// Known roles (mirroring deemix's own tagger.js): main_artist,
	// featuring, composer, author, engineer, mixer, producer, writer,
	// musicpublisher.
	SNG_CONTRIBUTORS map[string][]string `json:"SNG_CONTRIBUTORS"`
	// ARTISTS carries the main artist's own picture hash (ART_PICTURE) -
	// reading it here avoids a separate artist lookup just to save an
	// artist image file.
	ARTISTS []struct {
		ART_ID      json.Number `json:"ART_ID"`
		ART_PICTURE string      `json:"ART_PICTURE"`
	} `json:"ARTISTS"`
	FALLBACK *struct {
		SNG_ID json.Number `json:"SNG_ID"`
	} `json:"FALLBACK"`
}

// artistPictureHash returns the main artist's cover-image hash (for
// e-cdns-images.dzcdn.net/images/artist/<hash>/...) if song.getData
// included it, else "".
func (t gwTrack) artistPictureHash() string {
	for _, a := range t.ARTISTS {
		if a.ART_ID.String() == t.ART_ID.String() {
			return a.ART_PICTURE
		}
	}
	if len(t.ARTISTS) > 0 {
		return t.ARTISTS[0].ART_PICTURE
	}
	return ""
}

// gwAlbum is album.getData's response shape, trimmed similarly - only
// fetched as a supplementary lookup when a tag setting actually needs one
// of these fields (see download.go's downloadOneTrack).
//
// Deliberately NOT including GENRE_ID: gw's private API uses its own
// internal genre id space, disjoint from the public api.deezer.com genre
// ids - resolving it via fetchGenreName (which hits the public /genre/{id}
// endpoint) silently returns a real but wrong genre for almost every
// album (verified live: this album's gw GENRE_ID=27 round-tripped to "Thai
// Country" instead of the correct "Electro"). Genre name comes from
// fetchPublicAlbumGenre instead, which reads the public album endpoint's
// own already-resolved genres.data[].name.
type gwAlbum struct {
	LABEL_NAME   string      `json:"LABEL_NAME"`
	COPYRIGHT    string      `json:"COPYRIGHT"`
	NUMBER_TRACK json.Number `json:"NUMBER_TRACK"`
	NUMBER_DISK  json.Number `json:"NUMBER_DISK"`
}

func (c *dzClient) getAlbum(albID string) (*gwAlbum, error) {
	raw, err := c.gwCall("album.getData", map[string]any{"ALB_ID": albID})
	if err != nil {
		return nil, err
	}
	var a gwAlbum
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("parsing album data: %w", err)
	}
	return &a, nil
}

// publicAlbumInfo is the subset of Deezer's public (unauthenticated) album
// endpoint this package reads - genre name and barcode, neither of which
// gw's own album.getData carries in a directly usable form (see gwAlbum's
// doc comment for the GENRE_ID pitfall specifically).
type publicAlbumInfo struct {
	Genre   string
	Barcode string
}

func fetchPublicAlbumInfo(albID string) publicAlbumInfo {
	var info publicAlbumInfo
	resp, err := http.Get("https://api.deezer.com/album/" + albID)
	if err != nil {
		return info
	}
	defer resp.Body.Close()
	var data struct {
		UPC    string `json:"upc"`
		Genres struct {
			Data []struct {
				Name string `json:"name"`
			} `json:"data"`
		} `json:"genres"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return info
	}
	info.Barcode = data.UPC
	if len(data.Genres.Data) > 0 {
		info.Genre = data.Genres.Data[0].Name
	}
	return info
}

// gwLyrics is song.getLyrics's response shape.
type gwLyrics struct {
	LYRICS_TEXT string `json:"LYRICS_TEXT"`
}

func (c *dzClient) getLyrics(sngID string) (*gwLyrics, error) {
	raw, err := c.gwCall("song.getLyrics", map[string]any{"SNG_ID": sngID})
	if err != nil {
		return nil, err
	}
	var l gwLyrics
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, fmt.Errorf("parsing lyrics: %w", err)
	}
	return &l, nil
}

// fetchBPM reads a track's detected BPM from Deezer's public API - gw's
// song.getData doesn't carry it, only the public api.deezer.com one does.
// Many tracks report 0 (undetected); callers should treat that as "no BPM"
// rather than a real value.
func fetchBPM(sngID string) int {
	resp, err := http.Get("https://api.deezer.com/track/" + sngID)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	var data struct {
		BPM float64 `json:"bpm"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return 0
	}
	return int(data.BPM)
}

func (c *dzClient) getTrack(sngID string) (*gwTrack, error) {
	raw, err := c.gwCall("song.getData", map[string]any{"SNG_ID": sngID})
	if err != nil {
		return nil, err
	}
	var t gwTrack
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("parsing track data: %w", err)
	}
	return &t, nil
}

func (c *dzClient) getAlbumTracks(albID string) ([]gwTrack, error) {
	raw, err := c.gwCall("song.getListByAlbum", map[string]any{"ALB_ID": albID, "nb": -1})
	if err != nil {
		return nil, err
	}
	var data struct {
		Data []gwTrack `json:"data"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("parsing album tracks: %w", err)
	}
	return data.Data, nil
}

// getTrackURL asks media.deezer.com for a signed, time-limited download URL
// for one track at the given format ("FLAC"|"MP3_320"|"MP3_128"), encrypted
// with the BF_CBC_STRIPE cipher decryptStream expects. Mirrors deezer-js
// index.js's get_tracks_url.
func (c *dzClient) getTrackURL(trackToken, format string) (string, error) {
	c.mu.Lock()
	user := c.user
	c.mu.Unlock()
	if user.licenseToken == "" {
		return "", errNotLoggedIn
	}
	if (format == "FLAC" && !user.canStreamLossless) || (format == "MP3_320" && !user.canStreamHQ) {
		return "", errWrongLicense
	}

	payload := map[string]any{
		"license_token": user.licenseToken,
		"media": []map[string]any{{
			"type":    "FULL",
			"formats": []map[string]string{{"cipher": "BF_CBC_STRIPE", "format": format}},
		}},
		"track_tokens": []string{trackToken},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodPost, "https://media.deezer.com/v1/get_url", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", dzUserAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var out struct {
		Data []struct {
			Errors []struct {
				Code int `json:"code"`
			} `json:"errors"`
			Media []struct {
				Sources []struct {
					URL string `json:"url"`
				} `json:"sources"`
			} `json:"media"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decoding get_url response: %w", err)
	}
	if len(out.Data) == 0 {
		return "", errNoTrackURL
	}
	d := out.Data[0]
	if len(d.Errors) > 0 {
		if d.Errors[0].Code == 2002 {
			return "", errGeolocation
		}
		return "", fmt.Errorf("deezer get_url error code %d", d.Errors[0].Code)
	}
	if len(d.Media) == 0 || len(d.Media[0].Sources) == 0 {
		return "", errNoTrackURL
	}
	return d.Media[0].Sources[0].URL, nil
}
