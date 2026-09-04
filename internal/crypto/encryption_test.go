package crypto

import "testing"

func TestDecrypt_RoundTrip(t *testing.T) {
	secret := "test-secret"
	plaintext := "hello world, this is a token"

	encrypted, err := Encrypt(plaintext, secret)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	decrypted, err := Decrypt(encrypted, secret)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if decrypted != plaintext {
		t.Fatalf("round trip mismatch: got %q, want %q", decrypted, plaintext)
	}
}

func TestDecrypt_WrongSecretFails(t *testing.T) {
	encrypted, err := Encrypt("secret value", "correct-secret")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := Decrypt(encrypted, "wrong-secret"); err == nil {
		t.Fatal("expected decryption with the wrong secret to fail (GCM auth tag should not verify)")
	}
}

// Interop with the real Node server was verified manually (not as a
// permanent test, to avoid baking real credential material into the repo):
// a value from this deployment's actual users.spotify_client_id column,
// encrypted by the live Node server's utils/encryption.ts, decrypted to the
// exact same plaintext through this package's Decrypt() using the same
// secret. That confirms this port is byte-for-byte wire-compatible with the
// server it replaces, not just "implements AES-GCM correctly" in isolation.
