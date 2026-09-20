// collections.go adds real Plex Collection CRUD (DESIGN.md §11.11) - a
// separate object from a playlist, and distinct from discovery.go's
// GetLibraryCollections, which only reads collection *names* from the
// library's tag directory and predates any create/update/delete support.
//
// The full lifecycle (create -> read items -> add -> remove -> delete) has
// been smoke-tested end to end against a live Plex server on both a music
// (artist/track) library and a real movie library - the movie run caught
// one real bug, now fixed: Plex returns "smart" as a stringified "0"/"1"
// for movie collections but a real JSON bool for music ones, so Collection
// decodes it via flexBool (below) rather than a plain bool.
//
// Collections are always created as plain (non-smart, smart=0) Plex
// objects seeded with an app-computed ratingKey list, whether this app's
// own collection definition is "smart" (rule-based) or "manual": the rule
// evaluation happens app-side (librarysearch.go), so there is no reliance
// on Plex's own native smart-collection filter mechanism.
package plex

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type Collection struct {
	RatingKey  string   `json:"ratingKey"`
	Title      string   `json:"title"`
	ChildCount int      `json:"childCount"`
	Smart      flexBool `json:"smart"`
}

// flexBool tolerates Plex returning a boolean-ish field as either a real
// JSON bool or a stringified "0"/"1", confirmed to vary by library type
// (see package doc) rather than by any option this app controls.
type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	*b = s == "1" || s == "true"
	return nil
}

func decodeCollections(raw []json.RawMessage) ([]Collection, error) {
	out := make([]Collection, 0, len(raw))
	for _, r := range raw {
		var c Collection
		if err := json.Unmarshal(r, &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// GetCollections lists real Collection objects for a library section.
// GET /library/sections/{id}/collections - confirmed against a live server.
func (c *Client) GetCollections(libraryID string) ([]Collection, error) {
	mc, err := c.get("/library/sections/" + url.PathEscape(libraryID) + "/collections")
	if err != nil {
		return nil, err
	}
	return decodeCollections(mc.MediaContainer.Metadata)
}

// GetCollectionItems returns a collection's current members. Reuses Track/
// decodeTracks - the same ratingKey/title/genre/etc. shape covers movies
// and shows too, not just tracks.
//
// /library/metadata/{ratingKey}/children (the naming that would mirror a
// show/album's children) returns an empty container for a Collection
// object - /library/collections/{ratingKey}/items is the endpoint
// confirmed against a live server to actually return its members.
func (c *Client) GetCollectionItems(collectionRatingKey string) ([]Track, error) {
	mc, err := c.get("/library/collections/" + url.PathEscape(collectionRatingKey) + "/items")
	if err != nil {
		return nil, err
	}
	return decodeTracks(mc.MediaContainer.Metadata)
}

// CreateCollection creates a plain collection seeded with itemURIs (must be
// non-empty - Plex has no bare "empty collection" object to create ahead of
// content). itemType is Plex's numeric metadata type for the section's
// content: 1=movie, 2=show, 10=track.
// POST /library/collections?type={itemType}&title={title}&smart=0&sectionId={libraryID}&uri={batchURI}
// - confirmed against a live server (music/track library; see file header
// note on movie/show itemType).
func (c *Client) CreateCollection(libraryID string, itemType int, title string, itemURIs []string) (*Collection, error) {
	if len(itemURIs) == 0 {
		return nil, fmt.Errorf("cannot create a collection with no items")
	}
	batches, err := batchRatingKeyURIs(itemURIs)
	if err != nil {
		return nil, err
	}
	p := fmt.Sprintf("/library/collections?type=%d&title=%s&smart=0&sectionId=%s&uri=%s",
		itemType, url.QueryEscape(title), url.QueryEscape(libraryID), url.QueryEscape(batches[0]))
	mc, _, err := c.mutate(http.MethodPost, p)
	if err != nil {
		return nil, err
	}
	collections, err := decodeCollections(mc.MediaContainer.Metadata)
	if err != nil || len(collections) == 0 {
		return nil, fmt.Errorf("failed to create collection - no collection returned")
	}
	if len(batches) > 1 {
		if err := c.putCollectionItemBatches(collections[0].RatingKey, batches[1:]); err != nil {
			return nil, err
		}
	}
	return &collections[0], nil
}

// SetCollectionSortTitle sets and locks a collection's Plex sort title -
// Kometa's own convention for forcing collection display order (e.g. a
// "+1_" numeric prefix, see DESIGN.md §11.11's Sort Title field). Uses
// Plex's generic metadata-object field-edit convention (PUT
// /library/metadata/{ratingKey}?titleSort.value=...&titleSort.locked=1),
// the same one UploadPlaylistPoster already borrows for a different field -
// *not* independently live-verified against a real server the way that one
// was, so a wrong param name here is the first place to look if a sort
// title silently doesn't stick.
func (c *Client) SetCollectionSortTitle(ratingKey, sortTitle string) error {
	p := "/library/metadata/" + url.PathEscape(ratingKey) + "?titleSort.value=" + url.QueryEscape(sortTitle) + "&titleSort.locked=1"
	_, _, err := c.mutate(http.MethodPut, p)
	return err
}

// SetCollectionCustomOrder switches a collection's item display order from
// Plex's default (release date) to "Custom", so it actually displays in
// whatever order this app added its items - the point for an external-
// list/chart collection whose source is itself a ranking (DESIGN.md
// §11.11's "IMDb Top 250 is a best-to-worst order, not an arbitrary list"),
// where scheduler.go already preserves that source order end to end
// (resolveExternalListTargets -> targetKeys -> CreateCollection/
// AddToCollection all iterate in the source's own order, never a map).
//
// *Not* the same field-edit convention as SetCollectionSortTitle above -
// confirmed live against a real server: PUT /library/metadata/{ratingKey}
// ?collectionSort.value=2&collectionSort.locked=1 (titleSort's shape) 400s,
// and so does a bare ?sort=2 with no type param. The working call needs
// type=18 (Plex's metadata type for a Collection object, same family as
// CreateCollection's movie=1/show=2/track=10) alongside sort - without it
// Plex 400s the request outright rather than silently ignoring the param -
// 0=Release Date, 1=Alphabetical, 2=Custom (python-plexapi's
// Collection.sortUpdate() documents the same three values against this
// same endpoint, just doesn't need the type param since its own client
// already knows the object's type from having fetched it first).
func (c *Client) SetCollectionCustomOrder(ratingKey string) error {
	p := "/library/metadata/" + url.PathEscape(ratingKey) + "?type=18&sort=2"
	_, _, err := c.mutate(http.MethodPut, p)
	return err
}

// moveCollectionItem is Plex's manual-drag-reorder endpoint - PUT
// /library/collections/{collectionRatingKey}/items/{itemRatingKey}/move
// (optionally ?after={afterRatingKey}), confirmed live against a real
// server. afterRatingKey="" moves the item to the very front.
//
// This turned out to be the *only* thing that actually controls a
// collection's Custom item order: SetCollectionCustomOrder above just picks
// which order mode is active, it doesn't make bulk-added items land in
// request order - confirmed live that AddToCollection/CreateCollection's
// uri= batch add does not preserve the URI list's order at all (a fresh
// 193-item add came back sorted by release date, regardless of the order
// sent), so anything wanting real ranked order has to place every item
// with its own move call.
func (c *Client) moveCollectionItem(collectionRatingKey, itemRatingKey, afterRatingKey string) error {
	p := "/library/collections/" + url.PathEscape(collectionRatingKey) + "/items/" + url.PathEscape(itemRatingKey) + "/move"
	if afterRatingKey != "" {
		p += "?after=" + url.QueryEscape(afterRatingKey)
	}
	_, _, err := c.mutate(http.MethodPut, p)
	return err
}

// ReorderCollection arranges a collection's items to exactly match
// targetKeys' order end to end - one moveCollectionItem call per item,
// chaining each one after the previous (DESIGN.md §11.11: "IMDb Top 250 is
// a best-to-worst ranking, not an arbitrary list", and this generalizes to
// any ranked external source, not just IMDb). Skipped entirely when the
// collection's current order (restricted to just the items targetKeys
// actually contains, so members reconcileCollectionMembership is about to
// remove don't count against the comparison) already matches, so a refresh
// where the ranking didn't change doesn't pay for len(targetKeys) no-op
// API calls every single time - only a genuinely new build or a reshuffled
// source ranking does. Call after reconcileCollectionMembership, once every
// targetKeys entry is actually a member.
func (c *Client) ReorderCollection(collectionRatingKey string, targetKeys []string) error {
	current, err := c.GetCollectionItems(collectionRatingKey)
	if err != nil {
		return err
	}
	currentKeys := make([]string, len(current))
	for i, t := range current {
		currentKeys[i] = t.RatingKey
	}
	if alreadyOrdered(currentKeys, targetKeys) {
		return nil
	}
	var prev string
	for _, key := range targetKeys {
		if err := c.moveCollectionItem(collectionRatingKey, key, prev); err != nil {
			return err
		}
		prev = key
	}
	return nil
}

// alreadyOrdered reports whether current, filtered down to just the keys
// targetKeys also contains, is already in targetKeys' exact order - the
// cheap check ReorderCollection uses to skip a no-op reorder.
func alreadyOrdered(current, targetKeys []string) bool {
	want := make(map[string]bool, len(targetKeys))
	for _, k := range targetKeys {
		want[k] = true
	}
	filtered := current[:0:0]
	for _, k := range current {
		if want[k] {
			filtered = append(filtered, k)
		}
	}
	if len(filtered) != len(targetKeys) {
		return false
	}
	for i, k := range targetKeys {
		if filtered[i] != k {
			return false
		}
	}
	return true
}

// AddToCollection batches by 50 like AddToPlaylist.
// PUT /library/collections/{collectionRatingKey}/items?uri={batchURI} -
// confirmed against a live server.
func (c *Client) AddToCollection(collectionRatingKey string, itemURIs []string) error {
	batches, err := batchRatingKeyURIs(itemURIs)
	if err != nil {
		return err
	}
	return c.putCollectionItemBatches(collectionRatingKey, batches)
}

func (c *Client) putCollectionItemBatches(collectionRatingKey string, batches []string) error {
	for _, batchURI := range batches {
		p := "/library/collections/" + url.PathEscape(collectionRatingKey) + "/items?uri=" + url.QueryEscape(batchURI)
		if _, _, err := c.mutate(http.MethodPut, p); err != nil {
			return err
		}
	}
	return nil
}

// RemoveFromCollection uses the item's own ratingKey directly - unlike a
// playlist item, a collection member has no synthetic playlistItemID
// (collections are a real set, no duplicate membership). A 404 is treated
// as already-removed rather than a failure, matching
// RemoveMultipleFromPlaylist's tolerance for the same race.
// DELETE /library/collections/{collectionRatingKey}/items/{itemRatingKey} -
// confirmed against a live server.
func (c *Client) RemoveFromCollection(collectionRatingKey, itemRatingKey string) error {
	_, status, err := c.mutate(http.MethodDelete, "/library/collections/"+url.PathEscape(collectionRatingKey)+"/items/"+url.PathEscape(itemRatingKey))
	if status == http.StatusNotFound {
		return nil
	}
	return err
}

// DELETE /library/collections/{collectionRatingKey} - confirmed against a
// live server.
func (c *Client) DeleteCollection(collectionRatingKey string) error {
	_, _, err := c.mutate(http.MethodDelete, "/library/collections/"+url.PathEscape(collectionRatingKey))
	return err
}
