// legacy.go is the pre-license-token CDN URL scheme deemix falls back to
// when Settings.FeelingLucky is on and the modern get_track_url call fails
// - a straight port of deemix/decryption.js's generateCryptedStreamURL.
// Deezer's newer accounts/tracks are served entirely through get_track_url
// now, so this rarely fires, but it's cheap to keep as a last resort.
package deemix

import (
	"crypto/aes"
	"crypto/md5"
	"encoding/hex"
)

const legacyStreamKey = "jo6aey6haid2Teih"

// latin1Bytes mirrors Node's Buffer.from(s, 'binary') - one byte per
// character, not UTF-8. The JS source this is ported from hashes and
// encrypts its "¤"-joined strings under that encoding (crypto.js's _md5
// defaults to type='binary', and _ecbCrypt does the same), so a naive
// []byte(s) UTF-8 conversion in Go would multi-byte-encode "¤" (U+00A4)
// and silently produce a completely different hash/ciphertext.
func latin1Bytes(s string) []byte {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		out = append(out, byte(r))
	}
	return out
}

// legacyCryptedStreamURL guesses a BF_CBC_STRIPE-ciphered download URL
// directly from a track's own id/md5/media version/format, without ever
// asking Deezer for permission via get_url - it only works if the CDN
// still serves that exact combination, which isn't guaranteed.
func legacyCryptedStreamURL(sngID, md5Origin, mediaVersion, formatNumber string) (string, bool) {
	if md5Origin == "" {
		return "", false
	}
	urlPart := md5Origin + "¤" + formatNumber + "¤" + sngID + "¤" + mediaVersion
	sum := md5.Sum(latin1Bytes(urlPart))
	step2 := hex.EncodeToString(sum[:]) + "¤" + urlPart + "¤"

	// JS: '.'.repeat(16 - (step2.length % 16)) - always adds between 1 and
	// 16 dots, never zero, even when already block-aligned.
	step2Bytes := latin1Bytes(step2)
	padLen := 16 - (len(step2Bytes) % 16)
	for i := 0; i < padLen; i++ {
		step2Bytes = append(step2Bytes, '.')
	}

	block, err := aes.NewCipher([]byte(legacyStreamKey))
	if err != nil {
		return "", false
	}
	encrypted := make([]byte, len(step2Bytes))
	for i := 0; i < len(step2Bytes); i += aes.BlockSize {
		block.Encrypt(encrypted[i:i+aes.BlockSize], step2Bytes[i:i+aes.BlockSize])
	}
	urlPartHex := hex.EncodeToString(encrypted)

	return "https://e-cdns-proxy-" + string(md5Origin[0]) + ".dzcdn.net/mobile/1/" + urlPartHex, true
}
