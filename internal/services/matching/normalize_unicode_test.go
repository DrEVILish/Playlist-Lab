package matching

import "testing"

// Two unicode edge cases from matching-unicode.test.ts that
// matching_test.go's existing normalize/kana coverage doesn't already
// exercise (which only covers "non-Latin scripts survive" and kana
// romanization) - kept in their own file rather than folded into the
// existing suite, per the task brief not to restructure it.

// ß has no case-fold decomposition (it lowercases to itself and NFD leaves
// it alone), so before the explicit "ß"->"ss" replace it was stripped
// outright by the letters-only filter: "Straße" collapsed to "strae", not
// "strasse", so it silently stopped matching "Strasse".
func TestNormalizeForComparison_EszettFoldsToSS(t *testing.T) {
	got := normalizeForComparison("Straße")
	want := normalizeForComparison("Strasse")
	if got != want {
		t.Fatalf("expected eszett to fold to ss: %q vs %q", got, want)
	}
}

// Half-width katakana (the "ﾊﾅﾐｽﾞｷ" form found in some Latin-input source
// titles) has a compatibility (NFKD) decomposition to full-width katakana -
// normalizeForComparison must land both spellings on the same string so a
// half-width-katakana source title still matches Plex's full-width tag.
func TestNormalizeForComparison_HalfWidthKatakanaFoldsToFullWidth(t *testing.T) {
	got := normalizeForComparison("ﾊﾅﾐｽﾞｷ")
	want := normalizeForComparison("ハナミズキ")
	if got != want {
		t.Fatalf("expected half-width katakana to fold onto full-width: %q vs %q", got, want)
	}
}
