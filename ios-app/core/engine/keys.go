package engine

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"

	"golang.org/x/crypto/curve25519"
)

// WireGuard keys are 32 bytes. The app and the config files carry them as
// standard base64 (44 characters); wireguard-go's UAPI wants lowercase hex.

var errInvalidKey = errors.New("invalid key")

// decodeKey parses a WireGuard key: 32 bytes as standard base64, which is
// always 44 characters ending in one "=" (surrounding whitespace allowed).
func decodeKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if len(s) != 44 || s[43] != '=' || s[42] == '=' {
		return nil, errInvalidKey
	}
	return base64.StdEncoding.DecodeString(s)
}

// keyToHex converts a base64 key to UAPI hex.
func keyToHex(b64 string) (string, error) {
	k, err := decodeKey(b64)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(k), nil
}

// hexToKey converts a UAPI hex key back to base64, or "" if it isn't hex.
func hexToKey(h string) string {
	k, err := hex.DecodeString(h)
	if err != nil || len(k) != 32 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(k)
}

// GenerateKey makes a new WireGuard private key: 32 random bytes, clamped
// as X25519 requires (RFC 7748), in base64.
func GenerateKey() (string, error) {
	var k [32]byte
	if _, err := rand.Read(k[:]); err != nil {
		return "", err
	}
	k[0] &= 248
	k[31] &= 127
	k[31] |= 64
	return base64.StdEncoding.EncodeToString(k[:]), nil
}

// PublicKey derives the base64 public key from a base64 private key.
func PublicKey(priv string) (string, error) {
	k, err := decodeKey(priv)
	if err != nil {
		return "", errors.New("invalid private key")
	}
	pub, err := curve25519.X25519(k, curve25519.Basepoint)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(pub), nil
}
