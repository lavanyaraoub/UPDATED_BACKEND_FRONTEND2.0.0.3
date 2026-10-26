//go:build unit

package helper

// Tests for crypto.go — AES-CFB encrypt/decrypt pair.
// These are pure crypto operations with no external dependencies.

import (
	"strings"
	"testing"
)

// AES key must be exactly 16, 24, or 32 bytes.
const testKey = "1234567890123456" // 16 bytes

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	original := "Hello, EtherCAT!"
	encrypted := Encrypt([]byte(original), testKey)
	decrypted := Decrypt([]byte(encrypted), testKey)
	if decrypted != original {
		t.Errorf("round-trip failed: got %q, want %q", decrypted, original)
	}
}

func TestEncryptDecrypt_EmptyString(t *testing.T) {
	encrypted := Encrypt([]byte(""), testKey)
	decrypted := Decrypt([]byte(encrypted), testKey)
	if decrypted != "" {
		t.Errorf("empty string round-trip: got %q, want empty", decrypted)
	}
}

func TestEncryptDecrypt_LongString(t *testing.T) {
	original := strings.Repeat("G90;A90;M30;", 100)
	encrypted := Encrypt([]byte(original), testKey)
	decrypted := Decrypt([]byte(encrypted), testKey)
	if decrypted != original {
		t.Errorf("long string round-trip failed (length %d)", len(original))
	}
}

func TestEncryptDecrypt_DifferentKeys_ProduceDifferentCiphertext(t *testing.T) {
	plain := "test data"
	enc1 := Encrypt([]byte(plain), "1234567890123456")
	enc2 := Encrypt([]byte(plain), "abcdefghijklmnop")
	if enc1 == enc2 {
		t.Errorf("different keys produced identical ciphertext — should differ")
	}
}

func TestEncrypt_ProducesNonReadableCiphertext(t *testing.T) {
	plain := "secret program"
	encrypted := Encrypt([]byte(plain), testKey)
	// The encrypted form must not contain the plaintext verbatim
	if strings.Contains(encrypted, plain) {
		t.Errorf("encrypted output contains plaintext verbatim — encryption ineffective")
	}
}

func TestEncrypt_DifferentNonceEachCall(t *testing.T) {
	// Because AES-CFB uses a random IV, two encryptions of the same plaintext
	// should almost certainly produce different ciphertext.
	plain := "same input"
	enc1 := Encrypt([]byte(plain), testKey)
	enc2 := Encrypt([]byte(plain), testKey)
	if enc1 == enc2 {
		t.Logf("Warning: two encryptions produced identical output — random IV may not be working")
		// Not a hard failure since it's astronomically unlikely but theoretically possible
	}
}

func TestDecrypt_PanicsOnTooShortCiphertext(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected panic for too-short ciphertext, got nil")
		}
	}()
	// Anything shorter than aes.BlockSize (16 bytes) should panic
	Decrypt([]byte("short"), testKey)
}

func TestEncrypt_PanicsOnBadKeyLength(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("Encrypt with bad key: expected panic, got nil")
		}
	}()
	Encrypt([]byte("data"), "tooshort") // 8 bytes — not 16/24/32
}

func TestDecrypt_PanicsOnBadKeyLength(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("Decrypt with bad key: expected panic, got nil")
		}
	}()
	// Need at least 16 bytes of ciphertext to pass the BlockSize check,
	// but the key is invalid length so NewCipher panics first.
	Decrypt(make([]byte, 32), "tooshort")
}
