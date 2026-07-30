package token

import (
	"encoding/base64"
	"testing"
)

func TestGenerateIsRandomAndDecodable(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for range 1000 {
		tok, err := Generate()
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if seen[tok] {
			t.Fatalf("Generate returned a duplicate: %q", tok)
		}
		seen[tok] = true

		raw, err := base64.RawURLEncoding.DecodeString(tok)
		if err != nil {
			t.Fatalf("token is not valid raw-url base64: %v", err)
		}
		if len(raw) != entropyBytes {
			t.Fatalf("entropy = %d bytes, want %d", len(raw), entropyBytes)
		}
	}
}

// The encoding must survive a URL or QR payload without escaping.
func TestGenerateIsURLSafe(t *testing.T) {
	for range 100 {
		tok, err := Generate()
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		for _, r := range tok {
			ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9') || r == '-' || r == '_'
			if !ok {
				t.Fatalf("token %q contains %q, which is not URL-safe", tok, r)
			}
		}
	}
}

func TestHashIsStableAndDistinct(t *testing.T) {
	h := SHA256{}

	first, second := h.Hash("a"), h.Hash("a")
	if first != second {
		t.Error("hash is not stable")
	}
	if other := h.Hash("b"); first == other {
		t.Error("distinct tokens collided")
	}
	if len(first) != 64 {
		t.Errorf("hash length = %d, want 64 hex chars", len(first))
	}
	if first == "a" {
		t.Error("hash returned the plaintext")
	}
}

func TestEqual(t *testing.T) {
	h := SHA256{}
	if !Equal(h.Hash("x"), h.Hash("x")) {
		t.Error("Equal said identical hashes differ")
	}
	if Equal(h.Hash("x"), h.Hash("y")) {
		t.Error("Equal said different hashes match")
	}
	if Equal("", "nonempty") {
		t.Error("Equal matched an empty hash against a real one")
	}
}

// Compile-time check that SHA256 satisfies the interface consumers take.
var _ Hasher = SHA256{}
