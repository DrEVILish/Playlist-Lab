package matching

import "strings"

// romanizeKana ports matching.ts's romanizeKana(): wanakana only knows the
// phonetic reading of hiragana/katakana, passing kanji through untouched -
// a kanji's reading depends on context and needs a dictionary/morphological
// analyzer to resolve (see the kanjiToRomaji gap below). A leftover CJK
// ideograph in the output means the source wasn't purely kana, so
// romanizing it would just glue romaji onto untouched kanji and search Plex
// with garbage text - "", false tells the caller not to bother.
func romanizeKana(text string) (string, bool) {
	if text == "" || !isJapanese(text) {
		return "", false
	}
	romanized := toRomaji(text)
	if hanScriptPattern.MatchString(romanized) {
		return "", false
	}
	return romanized, true
}

// kanjiToRomaji is matching.ts's kuromoji-based dictionary reading for
// whatever text still contains kanji after romanizeKana gives up on it.
//
// ponytail: this is a deliberate gap, not an oversight. kuromoji's value is
// its bundled IPADIC dictionary - a morphological analyzer that resolves a
// kanji's reading from context - and there is no equivalent pure-Go
// dictionary bundled into this port yet (github.com/ikawaha/kagome is the
// closest like-for-like replacement, per the rewrite plan's R2, but adds a
// real dictionary-data footprint this phase hasn't taken on). The Node
// server's own comments already describe this reading as "a weaker,
// sometimes-wrong signal" fed through the same score/gate as everything
// else, never a "must be correct" shortcut - so returning nothing here
// degrades match quality only for kanji-heavy titles the kana pass alone
// can't already handle, it does not produce wrong matches. Wire in kagome
// here (and in buildKanjiVariants' caller) if that gap proves worth closing.
func kanjiToRomaji(text string) (string, bool) {
	return "", false
}

// isJapanese reports whether text contains any hiragana, katakana, or Han
// (kanji) character - wanakana.isJapanese() also treats full-width
// Latin/punctuation as "Japanese", which matters for wanakana's own
// round-tripping but not for this gate, so it's omitted here.
func isJapanese(text string) bool {
	for _, r := range text {
		if isHiragana(r) || isKatakana(r) || (r >= 0x4E00 && r <= 0x9FFF) {
			return true
		}
	}
	return false
}

func isHiragana(r rune) bool { return r >= 0x3040 && r <= 0x309F }
func isKatakana(r rune) bool { return r >= 0x30A0 && r <= 0x30FF }

// toRomaji converts every hiragana/katakana mora in s to Hepburn romaji,
// leaving any other character (kanji, Latin, punctuation) untouched. Long
// vowel mark (ー) repeats the preceding vowel; sokuon (っ/ッ) doubles the
// consonant that starts the following mora - both standard Hepburn rules.
func toRomaji(s string) string {
	runes := []rune(s)
	var b strings.Builder
	for i := 0; i < len(runes); i++ {
		r := runes[i]

		// Youon (combined mora): a base kana followed by a small ya/yu/yo.
		// Sokuon doubling (see below) already wrote its extra consonant
		// before this mora runs, so no special-casing is needed here.
		if i+1 < len(runes) {
			if combo, ok := kanaDigraphs[string([]rune{r, runes[i+1]})]; ok {
				b.WriteString(combo)
				i++
				continue
			}
		}

		switch {
		case r == 'っ' || r == 'ッ':
			// Sokuon: doubles the consonant of the *next* mora. Handled by
			// peeking ahead rather than here, since the next mora might
			// itself be a digraph (handled above) or a single kana (below).
			if i+1 < len(runes) {
				var next string
				if i+2 < len(runes) {
					if _, ok := kanaDigraphs[string([]rune{runes[i+1], runes[i+2]})]; ok {
						next = kanaDigraphs[string([]rune{runes[i+1], runes[i+2]})]
					}
				}
				if next == "" {
					next = kanaSingles[runes[i+1]]
				}
				if next != "" {
					b.WriteByte(next[0])
				}
			}
			continue
		case r == 'ー':
			cur := b.String()
			if cur != "" {
				last := cur[len(cur)-1]
				if isVowelByte(last) {
					b.WriteByte(last)
					continue
				}
			}
			b.WriteRune(r)
		default:
			if romaji, ok := kanaSingles[r]; ok {
				b.WriteString(romaji)
			} else {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

func isVowelByte(b byte) bool {
	switch b {
	case 'a', 'i', 'u', 'e', 'o':
		return true
	}
	return false
}

// kanaSingles covers the basic hiragana and katakana syllabaries (gojuon
// plus dakuten/handakuten variants). kanaDigraphs covers the youon
// (combined) forms, e.g. きゃ -> kya.
var kanaSingles = map[rune]string{
	'あ': "a", 'い': "i", 'う': "u", 'え': "e", 'お': "o",
	'か': "ka", 'き': "ki", 'く': "ku", 'け': "ke", 'こ': "ko",
	'が': "ga", 'ぎ': "gi", 'ぐ': "gu", 'げ': "ge", 'ご': "go",
	'さ': "sa", 'し': "shi", 'す': "su", 'せ': "se", 'そ': "so",
	'ざ': "za", 'じ': "ji", 'ず': "zu", 'ぜ': "ze", 'ぞ': "zo",
	'た': "ta", 'ち': "chi", 'つ': "tsu", 'て': "te", 'と': "to",
	'だ': "da", 'ぢ': "ji", 'づ': "zu", 'で': "de", 'ど': "do",
	'な': "na", 'に': "ni", 'ぬ': "nu", 'ね': "ne", 'の': "no",
	'は': "ha", 'ひ': "hi", 'ふ': "fu", 'へ': "he", 'ほ': "ho",
	'ば': "ba", 'び': "bi", 'ぶ': "bu", 'べ': "be", 'ぼ': "bo",
	'ぱ': "pa", 'ぴ': "pi", 'ぷ': "pu", 'ぺ': "pe", 'ぽ': "po",
	'ま': "ma", 'み': "mi", 'む': "mu", 'め': "me", 'も': "mo",
	'や': "ya", 'ゆ': "yu", 'よ': "yo",
	'ら': "ra", 'り': "ri", 'る': "ru", 'れ': "re", 'ろ': "ro",
	'わ': "wa", 'を': "wo", 'ん': "n",
	'ぁ': "a", 'ぃ': "i", 'ぅ': "u", 'ぇ': "e", 'ぉ': "o",
	'ゔ': "vu",

	'ア': "a", 'イ': "i", 'ウ': "u", 'エ': "e", 'オ': "o",
	'カ': "ka", 'キ': "ki", 'ク': "ku", 'ケ': "ke", 'コ': "ko",
	'ガ': "ga", 'ギ': "gi", 'グ': "gu", 'ゲ': "ge", 'ゴ': "go",
	'サ': "sa", 'シ': "shi", 'ス': "su", 'セ': "se", 'ソ': "so",
	'ザ': "za", 'ジ': "ji", 'ズ': "zu", 'ゼ': "ze", 'ゾ': "zo",
	'タ': "ta", 'チ': "chi", 'ツ': "tsu", 'テ': "te", 'ト': "to",
	'ダ': "da", 'ヂ': "ji", 'ヅ': "zu", 'デ': "de", 'ド': "do",
	'ナ': "na", 'ニ': "ni", 'ヌ': "nu", 'ネ': "ne", 'ノ': "no",
	'ハ': "ha", 'ヒ': "hi", 'フ': "fu", 'ヘ': "he", 'ホ': "ho",
	'バ': "ba", 'ビ': "bi", 'ブ': "bu", 'ベ': "be", 'ボ': "bo",
	'パ': "pa", 'ピ': "pi", 'プ': "pu", 'ペ': "pe", 'ポ': "po",
	'マ': "ma", 'ミ': "mi", 'ム': "mu", 'メ': "me", 'モ': "mo",
	'ヤ': "ya", 'ユ': "yu", 'ヨ': "yo",
	'ラ': "ra", 'リ': "ri", 'ル': "ru", 'レ': "re", 'ロ': "ro",
	'ワ': "wa", 'ヲ': "wo", 'ン': "n",
	'ァ': "a", 'ィ': "i", 'ゥ': "u", 'ェ': "e", 'ォ': "o",
	'ヴ': "vu",
}

var kanaDigraphs = buildKanaDigraphs()

func buildKanaDigraphs() map[string]string {
	// Base kana mapped to the consonant it contributes to a youon (combined
	// mora) - e.g. き+ゃ -> "ky"+"a" -> "kya".
	baseConsonants := map[string]string{
		"き": "ky", "ぎ": "gy", "し": "sh", "じ": "j", "ち": "ch", "ぢ": "j",
		"に": "ny", "ひ": "hy", "び": "by", "ぴ": "py", "み": "my", "り": "ry",
		"キ": "ky", "ギ": "gy", "シ": "sh", "ジ": "j", "チ": "ch", "ヂ": "j",
		"ニ": "ny", "ヒ": "hy", "ビ": "by", "ピ": "py", "ミ": "my", "リ": "ry",
	}
	smallY := map[string]string{"ゃ": "a", "ゅ": "u", "ょ": "o", "ャ": "a", "ュ": "u", "ョ": "o"}

	out := map[string]string{}
	for base, consonant := range baseConsonants {
		for small, vowel := range smallY {
			out[base+small] = consonant + vowel
		}
	}
	return out
}

// buildKanaVariants ports matching.ts's buildKanaVariants(): a single-
// element slice (just track itself) for anything that isn't pure kana, or
// up to four variants (original, romanized title, romanized artist, both)
// for a track whose title and/or artist round-trip cleanly through kana
// romanization.
func buildKanaVariants(track Track) []Track {
	romanizedTitle, titleOK := romanizeKana(track.Title)
	romanizedArtist, artistOK := romanizeKana(track.Artist)
	if !titleOK && !artistOK {
		return []Track{track}
	}

	variants := []Track{track}
	if titleOK {
		v := track
		v.Title = romanizedTitle
		variants = append(variants, v)
	}
	if artistOK {
		v := track
		v.Artist = romanizedArtist
		variants = append(variants, v)
	}
	if titleOK && artistOK {
		v := track
		v.Title, v.Artist = romanizedTitle, romanizedArtist
		variants = append(variants, v)
	}
	return variants
}

// buildKanjiVariants ports matching.ts's buildKanjiVariants() - currently a
// no-op, see kanjiToRomaji's doc comment for why.
func buildKanjiVariants(track Track) []Track {
	kanjiTitle, titleOK := kanjiToRomaji(track.Title)
	kanjiArtist, artistOK := kanjiToRomaji(track.Artist)
	if !titleOK && !artistOK {
		return nil
	}
	var variants []Track
	if titleOK {
		v := track
		v.Title = kanjiTitle
		variants = append(variants, v)
	}
	if artistOK {
		v := track
		v.Artist = kanjiArtist
		variants = append(variants, v)
	}
	if titleOK && artistOK {
		v := track
		v.Title, v.Artist = kanjiTitle, kanjiArtist
		variants = append(variants, v)
	}
	return variants
}
