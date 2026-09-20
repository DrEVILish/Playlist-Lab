package apple

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// generateDeveloperToken ports apple-target.ts's generateDeveloperToken():
// a MusicKit ES256 JWT signed with the developer's private key, valid 6
// months.
//
// Node's crypto.sign() with dsaEncoding:'ieee-p1363' produces the raw,
// fixed-width r||s signature JWT's ES256 alg requires (each 32 bytes for
// P-256, zero-padded, concatenated) - Go's ecdsa.Sign returns r and s as
// separate big.Ints with no such padding, and there's no stdlib "sign as
// JWT" helper, so the fixed-width encoding is done by hand below rather
// than pulling in a JWT library for one token type this app needs.
func generateDeveloperToken(teamID, keyID, privateKeyPEM string) (string, error) {
	if teamID == "" || keyID == "" || privateKeyPEM == "" {
		return "", errors.New("Apple Music credentials not configured (APPLE_TEAM_ID, APPLE_KEY_ID, APPLE_PRIVATE_KEY)")
	}

	key, err := parseECPrivateKey(privateKeyPEM)
	if err != nil {
		return "", fmt.Errorf("invalid Apple Music private key: %w", err)
	}

	header, err := base64URLJSON(struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}{"ES256", keyID})
	if err != nil {
		return "", err
	}
	now := time.Now().Unix()
	payload, err := base64URLJSON(struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}{teamID, now, now + 15_552_000})
	if err != nil {
		return "", err
	}

	signingInput := header + "." + payload
	hash := sha256.Sum256([]byte(signingInput))

	r, s, err := ecdsa.Sign(rand.Reader, key, hash[:])
	if err != nil {
		return "", err
	}
	signature := base64.RawURLEncoding.EncodeToString(append(padTo32(r), padTo32(s)...))

	return signingInput + "." + signature, nil
}

func parseECPrivateKey(pemStr string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("failed to decode PEM block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("not an EC private key")
	}
	return key, nil
}

// padTo32 left-pads a signature component to the fixed 32-byte width P-256
// requires - big.Int.Bytes() drops leading zero bytes, which JWT's raw
// r||s encoding must not.
func padTo32(n *big.Int) []byte {
	b := n.Bytes()
	if len(b) >= 32 {
		return b[len(b)-32:]
	}
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return out
}

// base64URLJSON marshals v to base64url with no padding, as JWT requires.
func base64URLJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
