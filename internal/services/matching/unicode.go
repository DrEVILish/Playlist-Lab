package matching

import "golang.org/x/text/unicode/norm"

func nfkd(s string) string { return norm.NFKD.String(s) }
func nfc(s string) string  { return norm.NFC.String(s) }

// stripCombiningMarks drops NFKD's decomposed combining marks (accents),
// same range as matching.ts's /[̀-ͯ]/g.
func stripCombiningMarks(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r >= 0x0300 && r <= 0x036f {
			continue
		}
		out = append(out, r)
	}
	return string(out)
}
