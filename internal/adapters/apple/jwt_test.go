package apple

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

// TestGenerateDeveloperToken_SignatureVerifies is the strongest check
// available without real Apple credentials: generate a throwaway P-256 key,
// sign a token with it, then independently re-verify that signature using
// only stdlib crypto/ecdsa - proving the raw r||s (IEEE P1363) encoding this
// file hand-rolls (to match what Node's dsaEncoding:'ieee-p1363' produces)
// round-trips correctly, and that a JWT verifier reading the standard
// format would accept it.
func TestGenerateDeveloperToken_SignatureVerifies(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})

	token, err := generateDeveloperToken("TEAM123", "KEY456", string(pemBytes))
	if err != nil {
		t.Fatalf("generateDeveloperToken: %v", err)
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("expected a 3-part JWT, got %d parts: %q", len(parts), token)
	}

	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	decodeJSONPart(t, parts[0], &header)
	if header.Alg != "ES256" || header.Kid != "KEY456" {
		t.Fatalf("unexpected header: %+v", header)
	}

	var payload struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}
	decodeJSONPart(t, parts[1], &payload)
	if payload.Iss != "TEAM123" {
		t.Fatalf("expected iss=TEAM123, got %q", payload.Iss)
	}
	if payload.Exp-payload.Iat != 15_552_000 {
		t.Fatalf("expected a 6-month (15,552,000s) validity window, got %d", payload.Exp-payload.Iat)
	}
	if time.Unix(payload.Iat, 0).After(time.Now().Add(time.Minute)) {
		t.Fatalf("iat should be roughly now, got %v", time.Unix(payload.Iat, 0))
	}

	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decoding signature: %v", err)
	}
	if len(sigBytes) != 64 {
		t.Fatalf("expected a 64-byte raw r||s ES256 signature, got %d bytes", len(sigBytes))
	}
	r := new(big.Int).SetBytes(sigBytes[:32])
	s := new(big.Int).SetBytes(sigBytes[32:])

	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(&key.PublicKey, hash[:], r, s) {
		t.Fatal("signature does not verify against the signing key - the raw r||s encoding is wrong")
	}
}

func decodeJSONPart(t *testing.T, part string, v any) {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(part)
	if err != nil {
		t.Fatalf("decoding JWT part: %v", err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("unmarshalling JWT part: %v", err)
	}
}

func TestGenerateDeveloperToken_MissingCredentials(t *testing.T) {
	if _, err := generateDeveloperToken("", "", ""); err == nil {
		t.Fatal("expected an error when Apple credentials are not configured")
	}
}
