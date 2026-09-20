package deemix

import (
	"bytes"
	"crypto/cipher"
	"testing"

	"golang.org/x/crypto/blowfish"
)

// TestBlowfishKey_MatchesReferenceImplementation locks blowfishKey's output
// against deemix's own vendored JS implementation (deemix/utils/crypto.js's
// generateBlowfishKey), captured by running:
//
//	node -e "console.log(JSON.stringify(Array.from(Buffer.from(require('./deemix/utils/crypto.js').generateBlowfishKey('3135556'), 'binary'))))"
//
// from server/node_modules/deemix in the deemix-server checkout. A wrong key
// here decrypts every downloaded track into noise, so this is the one check
// that has to hold for any of the rest of the download pipeline to matter.
func TestBlowfishKey_MatchesReferenceImplementation(t *testing.T) {
	want := []byte{108, 108, 102, 107, 57, 102, 44, 55, 101, 37, 117, 96, 60, 100, 52, 57}
	got := blowfishKey("3135556")
	if !bytes.Equal(got, want) {
		t.Fatalf("blowfishKey(%q) = %v, want %v (from deemix's own JS implementation)", "3135556", got, want)
	}
}

func TestDecryptStream_RoundTripsAgainstRealBlowfishCBCStripe(t *testing.T) {
	trackID := "3135556"
	key := blowfishKey(trackID)
	block, err := blowfish.NewCipher(key)
	if err != nil {
		t.Fatalf("blowfish.NewCipher: %v", err)
	}

	// Build a plaintext long enough to span several 3-chunk (6144-byte)
	// groups plus a short final tail, then encrypt it exactly the way
	// deezer's CDN would have (first 2048 of every 3 chunks enciphered,
	// the other two left alone) so decryptStream has something real to
	// undo.
	plain := make([]byte, 6144*2+3000)
	for i := range plain {
		plain[i] = byte(i + 1) // +1 so byte 0 of the stream is never 0x00 - a real
		// encoded audio file never starts with a null byte, and starting with one
		// here would make depad (correctly) strip it, which isn't what this test
		// is checking.
	}
	encrypted := append([]byte(nil), plain...)
	for offset := 0; offset+2048 <= len(encrypted); offset += 2048 * 3 {
		cbc := cipher.NewCBCEncrypter(block, blowfishIV)
		cbc.CryptBlocks(encrypted[offset:offset+2048], encrypted[offset:offset+2048])
	}

	var out bytes.Buffer
	if err := decryptStream(&out, bytes.NewReader(encrypted), trackID); err != nil {
		t.Fatalf("decryptStream: %v", err)
	}
	if !bytes.Equal(out.Bytes(), plain) {
		t.Fatalf("decryptStream did not recover the original plaintext (got %d bytes, want %d)", out.Len(), len(plain))
	}
}

func TestDepad_StripsLeadingZerosUnlessMP4(t *testing.T) {
	in := append([]byte{0, 0, 0, 0, 'x', 'y', 'z'}, []byte{1, 2, 3}...)
	got := depad(in)
	want := []byte{'x', 'y', 'z', 1, 2, 3}
	if !bytes.Equal(got, want) {
		t.Fatalf("depad stripped leading zeros incorrectly: got %v, want %v", got, want)
	}

	mp4 := []byte{0, 0, 0, 0, 'f', 't', 'y', 'p', 1, 2}
	if got := depad(mp4); !bytes.Equal(got, mp4) {
		t.Fatalf("depad must not touch a real ftyp box: got %v, want unchanged %v", got, mp4)
	}
}
