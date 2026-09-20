package deemix

import (
	"bytes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/hex"
	"io"

	"golang.org/x/crypto/blowfish"
)

// blowfishSecret is deemix's fixed, publicly-known salt for deriving a
// per-track decryption key - not a real secret, just part of the published
// algorithm (see deemix/utils/crypto.js's generateBlowfishKey).
const blowfishSecret = "g4el58wc0zvf9na1"

// blowfishKey derives the per-track Blowfish key deezer's BF_CBC_STRIPE
// cipher uses: the ASCII-hex MD5 of the track's numeric id, XORed against
// itself split down the middle and against the fixed secret above.
func blowfishKey(trackID string) []byte {
	sum := md5.Sum([]byte(trackID))
	hexSum := hex.EncodeToString(sum[:]) // 32 ascii hex chars
	key := make([]byte, 16)
	for i := 0; i < 16; i++ {
		key[i] = hexSum[i] ^ hexSum[i+16] ^ blowfishSecret[i]
	}
	return key
}

// blowfishIV is the fixed initialization vector deezer's stream cipher
// uses for every track (see deemix/decryption.js's streamTrack).
var blowfishIV = []byte{0, 1, 2, 3, 4, 5, 6, 7}

// decryptStream copies src to dst, decrypting it as a deezer BF_CBC_STRIPE
// track stream: the source is split into 2048-byte blocks, and only every
// first block of each group of three is actually encrypted (Blowfish-CBC,
// no padding) - the other two of each three pass through untouched. This
// mirrors deemix/decryption.js's streamTrack+decryptChunk exactly (chunk
// striping is deezer's own scheme, not something this port chose).
func decryptStream(dst io.Writer, src io.Reader, trackID string) error {
	block, err := blowfish.NewCipher(blowfishKey(trackID))
	if err != nil {
		return err
	}

	const chunkSize = 2048
	const groupSize = chunkSize * 3
	buf := make([]byte, groupSize)
	firstChunk := true

	for {
		n, readErr := io.ReadFull(src, buf)
		if n > 0 {
			out := make([]byte, n)
			copy(out, buf[:n])
			if n >= chunkSize {
				cbc := cipher.NewCBCDecrypter(block, blowfishIV)
				cbc.CryptBlocks(out[:chunkSize], out[:chunkSize])
			}
			if firstChunk {
				out = depad(out)
				firstChunk = false
			}
			if _, err := dst.Write(out); err != nil {
				return err
			}
		}
		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

// depad strips leading zero-padding some CDN responses prefix the very
// first chunk with - unless what follows those zeros is actually the start
// of an "ftyp" MP4 box (a real file, not padding). Mirrors
// deemix/decryption.js's depadder.
func depad(chunk []byte) []byte {
	if len(chunk) == 0 || chunk[0] != 0 {
		return chunk
	}
	if len(chunk) >= 8 && bytes.Equal(chunk[4:8], []byte("ftyp")) {
		return chunk
	}
	i := 0
	for i < len(chunk) && chunk[i] == 0 {
		i++
	}
	return chunk[i:]
}
