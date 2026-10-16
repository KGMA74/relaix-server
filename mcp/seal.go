package mcp

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

// sealer encrypts what the OAuth flow hands out — client ids, codes, tokens —
// so the gateway keeps none of it: a token carries the API key it stands for,
// sealed with AES-256-GCM, and opening it is all there is to checking it.
//
// Consequences, chosen on purpose:
//   - no table, no migration, nothing lost on a restart or shared between
//     replicas beyond the secret;
//   - every request still authenticates the key inside, so revoking that key,
//     or suspending its account, cuts the assistant off at once;
//   - a token cannot be revoked on its own. Revoke the key it was made from.
//
// The kind of token is the additional data: a refresh token does not open as
// an access token, nor a client id as a code.
type sealer struct {
	aead cipher.AEAD
}

// newSealer derives the encryption key from secret. An empty secret gets a
// random one: tokens then last as long as the process.
func newSealer(secret []byte) (*sealer, error) {
	if len(secret) == 0 {
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, err
		}
	}
	key, err := hkdf.Key(sha256.New, secret, nil, "relaix mcp oauth v1", 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &sealer{aead: aead}, nil
}

var errUnsealed = errors.New("mcp: token does not open")

// seal encodes v as JSON and returns prefix + base64url(nonce ‖ ciphertext).
func (s *sealer) seal(kind, prefix string, v any) (string, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := s.aead.Seal(nonce, nonce, plain, []byte(kind))
	return prefix + base64.RawURLEncoding.EncodeToString(out), nil
}

// open reverses seal into v.
func (s *sealer) open(kind, prefix, token string, v any) error {
	rest, ok := strings.CutPrefix(token, prefix)
	if !ok {
		return errUnsealed
	}
	raw, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil || len(raw) < s.aead.NonceSize() {
		return errUnsealed
	}
	n := s.aead.NonceSize()
	plain, err := s.aead.Open(nil, raw[:n], raw[n:], []byte(kind))
	if err != nil {
		return errUnsealed
	}
	if err := json.Unmarshal(plain, v); err != nil {
		return errUnsealed
	}
	return nil
}
