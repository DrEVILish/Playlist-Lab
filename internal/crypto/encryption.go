// Package crypto ports utils/encryption.ts byte-for-byte: AES-256-GCM with
// a PBKDF2-SHA256 (100,000 iterations) derived key, so values encrypted by
// the Node server (OAuth client secrets, API keys) remain readable after
// cutover to this Go binary against the same database file. The wire format
// is base64(salt(64) || iv(16) || authTag(16) || ciphertext) - Node stores
// the GCM auth tag separately from the ciphertext, so Decrypt has to
// re-append it before calling Go's cipher.AEAD.Open, which expects the tag
// appended to the ciphertext rather than passed separately.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"

	"golang.org/x/crypto/pbkdf2"
)

const (
	ivLength      = 16
	authTagLength = 16
	saltLength    = 64
	keyLength     = 32
	pbkdf2Iters   = 100000
)

func deriveKey(secret string, salt []byte) []byte {
	return pbkdf2.Key([]byte(secret), salt, pbkdf2Iters, keyLength, sha256.New)
}

// Encrypt matches encrypt() in utils/encryption.ts.
func Encrypt(plaintext, secret string) (string, error) {
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	iv := make([]byte, ivLength)
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}

	gcm, err := newGCM(secret, salt, ivLength)
	if err != nil {
		return "", err
	}

	// Seal appends the auth tag to the end of the ciphertext; Node keeps it
	// as a separate field, so split it back out to match encrypt()'s
	// salt+iv+authTag+ciphertext layout.
	sealed := gcm.Seal(nil, iv, []byte(plaintext), nil)
	ciphertext := sealed[:len(sealed)-authTagLength]
	authTag := sealed[len(sealed)-authTagLength:]

	combined := make([]byte, 0, saltLength+ivLength+authTagLength+len(ciphertext))
	combined = append(combined, salt...)
	combined = append(combined, iv...)
	combined = append(combined, authTag...)
	combined = append(combined, ciphertext...)
	return base64.StdEncoding.EncodeToString(combined), nil
}

// Decrypt matches decrypt() in utils/encryption.ts.
func Decrypt(encrypted, secret string) (string, error) {
	combined, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		return "", err
	}
	if len(combined) < saltLength+ivLength+authTagLength {
		return "", errors.New("encrypted value too short")
	}

	salt := combined[:saltLength]
	iv := combined[saltLength : saltLength+ivLength]
	authTag := combined[saltLength+ivLength : saltLength+ivLength+authTagLength]
	ciphertext := combined[saltLength+ivLength+authTagLength:]

	gcm, err := newGCM(secret, salt, ivLength)
	if err != nil {
		return "", err
	}

	// Go's AEAD.Open expects the tag appended to the ciphertext, not passed
	// separately.
	sealed := append(append([]byte{}, ciphertext...), authTag...)
	plaintext, err := gcm.Open(nil, iv, sealed, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func newGCM(secret string, salt []byte, nonceSize int) (cipher.AEAD, error) {
	key := deriveKey(secret, salt)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithNonceSize(block, nonceSize)
}
