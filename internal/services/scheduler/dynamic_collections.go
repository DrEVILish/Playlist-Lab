// dynamic_collections.go realizes a Dynamic Collection set (Kometa's
// "dynamic_collections" feature - https://kometa.wiki/en/latest/files/dynamic/)
// into real Smart collections: one distinct value of a library facet
// (genre, decade, year, content rating, studio/network, actor, mood, style)
// becomes one ordinary `collections` row with a single matching rule,
// reusing RefreshCollection - the exact same engine a hand-built Smart
// collection already goes through - rather than a parallel membership
// pipeline. Kept deliberately smaller than Kometa's own dynamic_collections:
// no addons/other_name catch-all bucket, no per-key template_variables, no
// remove_prefix/remove_suffix/key_name_override - include/exclude plus a
// <<key_name>> title format covers the common case, and everything else is
// still just a normal collection afterward (rename, delete, re-point its
// own sort title) editable the regular way.
package scheduler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/plex"
)

// fetchFacetKeys pulls the distinct values of one library facet, the same
// primitive Collections' own Smart rule fields already filter against
// (scheduler.go's resolveCollectionTargets) - a Dynamic Collection set's
// facet_type is one of the exact same field names, so generation and
// matching agree on what each key means. "decade" has no facet endpoint of
// its own on a real Plex server; it's derived here from GetLibraryYears.
func fetchFacetKeys(client *plex.Client, dyn *db.DynamicCollection) ([]string, error) {
	switch dyn.FacetType {
	case "genre":
		return client.GetLibraryGenres(dyn.LibrarySectionID), nil
	case "mood":
		return client.GetLibraryMoods(dyn.LibrarySectionID), nil
	case "style":
		return client.GetLibraryStyles(dyn.LibrarySectionID), nil
	case "studio":
		return client.GetLibraryStudios(dyn.LibrarySectionID), nil
	case "content_rating":
		return client.GetLibraryContentRatings(dyn.LibrarySectionID), nil
	case "actor":
		return client.GetLibraryActors(dyn.LibrarySectionID), nil
	case "director":
		return client.GetLibraryDirectors(dyn.LibrarySectionID), nil
	case "writer":
		return client.GetLibraryWriters(dyn.LibrarySectionID), nil
	case "year":
		return client.GetLibraryYears(dyn.LibrarySectionID), nil
	case "decade":
		years := client.GetLibraryYears(dyn.LibrarySectionID)
		seen := map[string]bool{}
		var decades []string
		for _, y := range years {
			n, err := strconv.Atoi(y)
			if err != nil {
				continue
			}
			d := strconv.Itoa(n - (n % 10))
			if !seen[d] {
				seen[d] = true
				decades = append(decades, d)
			}
		}
		sort.Strings(decades)
		return decades, nil
	default:
		return nil, fmt.Errorf("unsupported dynamic collection type %q", dyn.FacetType)
	}
}

// filterKeys applies a Dynamic Collection set's include/exclude lists
// (mutually exclusive, same as Kometa's own - include wins if both are
// somehow set). Matching is case-insensitive since a facet value's exact
// casing on the Plex server isn't something a user typing "action" into a
// form should have to get right.
func filterKeys(keys []string, dyn *db.DynamicCollection) []string {
	include := dyn.ParsedInclude()
	if len(include) > 0 {
		want := map[string]bool{}
		for _, k := range include {
			want[strings.ToLower(k)] = true
		}
		out := keys[:0:0]
		for _, k := range keys {
			if want[strings.ToLower(k)] {
				out = append(out, k)
			}
		}
		return out
	}
	exclude := dyn.ParsedExclude()
	if len(exclude) == 0 {
		return keys
	}
	skip := map[string]bool{}
	for _, k := range exclude {
		skip[strings.ToLower(k)] = true
	}
	out := keys[:0:0]
	for _, k := range keys {
		if !skip[strings.ToLower(k)] {
			out = append(out, k)
		}
	}
	return out
}

// titleFor applies the Kometa-style "<<key_name>>" placeholder convention
// (DESIGN.md §11.11's Dynamic Collections builder) - a title format missing
// the placeholder entirely just falls back to the bare key, rather than
// generating every collection under the same literal name.
func titleFor(dyn *db.DynamicCollection, key string) string {
	if !strings.Contains(dyn.TitleFormat, "<<key_name>>") {
		return key
	}
	return strings.ReplaceAll(dyn.TitleFormat, "<<key_name>>", key)
}

// GenerateDynamicCollections is the "Generate Now" action (and, on the
// same button, every re-run): fetch the current facet keys, create a Smart
// collection for any new one and refresh every existing one's membership in
// place, then - only in "sync" mode - delete any previously-generated
// collection whose key no longer appears (e.g. the last movie in a genre
// was removed from the library). Deletion never touches the Plex-side
// collection itself, matching Collections' own plain-delete default
// (collections.go's delete handler) - a rarer explicit sweep is left to the
// caller via deleteStaleFromPlex.
func GenerateDynamicCollections(sqlDB *sql.DB, client *plex.Client, dyn *db.DynamicCollection, server *db.UserServer, deleteStaleFromPlex bool) (created, refreshed, removed int, errs []error) {
	keys, err := fetchFacetKeys(client, dyn)
	if err != nil {
		return 0, 0, 0, []error{err}
	}
	keys = filterKeys(keys, dyn)

	existing, err := db.GetGeneratedCollections(sqlDB, dyn.ID)
	if err != nil {
		return 0, 0, 0, []error{err}
	}
	existingByKey := map[string]db.Collection{}
	for _, c := range existing {
		if c.DynamicKey.Valid {
			existingByKey[c.DynamicKey.String] = c
		}
	}

	seen := map[string]bool{}
	for _, key := range keys {
		seen[key] = true
		title := titleFor(dyn, key)

		if row, ok := existingByKey[key]; ok {
			if title != row.Name {
				_ = db.UpdateCollection(sqlDB, row.ID, db.CollectionUpdate{Name: &title})
				row.Name = title
			}
			if _, _, err := RefreshCollection(sqlDB, client, &row, server); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", key, err))
				continue
			}
			refreshed++
			continue
		}

		rulesJSON, _ := json.Marshal([]db.Rule{{Field: dyn.FacetType, Operator: "is", Value: key}})
		row, err := db.CreateGeneratedCollection(sqlDB, *dyn, title, "", key, string(rulesJSON))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", key, err))
			continue
		}
		if _, _, err := RefreshCollection(sqlDB, client, row, server); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", key, err))
			continue
		}
		created++
	}

	if dyn.SyncMode == "sync" {
		for key, row := range existingByKey {
			if seen[key] {
				continue
			}
			if deleteStaleFromPlex && row.PlexCollectionID.Valid {
				_ = client.DeleteCollection(row.PlexCollectionID.String)
			}
			if err := db.DeleteCollection(sqlDB, row.ID); err != nil {
				errs = append(errs, fmt.Errorf("removing stale %q: %w", key, err))
				continue
			}
			removed++
		}
	}

	return created, refreshed, removed, errs
}
