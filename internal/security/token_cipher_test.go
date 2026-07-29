package security

import (
	"encoding/base64"
	"testing"
)

func TestTokenCipherRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	cipher, err := NewTokenCipher(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatalf("NewTokenCipher() error = %v", err)
	}

	plain := "refresh-token-123"
	enc, err := cipher.Encrypt(plain)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	if enc == plain {
		t.Fatalf("Encrypt() returned plaintext")
	}

	dec, err := cipher.Decrypt(enc)
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if dec != plain {
		t.Fatalf("Decrypt() = %q, want %q", dec, plain)
	}
}
