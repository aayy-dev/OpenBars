// Package wgkey makes and converts WireGuard (Curve25519) keys.
package wgkey

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"

	"golang.org/x/crypto/curve25519"
)

// Generate returns a new private key, base64 as in WireGuard configs.
func Generate() (string, error) {
	var k [32]byte
	if _, err := rand.Read(k[:]); err != nil {
		return "", err
	}
	// Clamp as Curve25519 expects (what `wg genkey` does).
	k[0] &= 248
	k[31] = (k[31] & 127) | 64
	return base64.StdEncoding.EncodeToString(k[:]), nil
}

// Public derives the base64 public key from a base64 private key.
func Public(private string) (string, error) {
	k, err := decode(private)
	if err != nil {
		return "", err
	}
	pub, err := curve25519.X25519(k, curve25519.Basepoint)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(pub), nil
}

// Hex converts a base64 key to the hex form wireguard-go's IPC wants.
func Hex(key string) (string, error) {
	k, err := decode(key)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(k), nil
}

// Valid reports whether key is a base64 32-byte key.
func Valid(key string) bool {
	_, err := decode(key)
	return err == nil
}

// Short is the first 8 characters, for logs ("abcdEFGH…").
func Short(key string) string {
	if len(key) <= 8 {
		return key
	}
	return key[:8] + "…"
}

func decode(key string) ([]byte, error) {
	k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(key))
	if err != nil || len(k) != 32 {
		return nil, errors.New("not a WireGuard key")
	}
	return k, nil
}
