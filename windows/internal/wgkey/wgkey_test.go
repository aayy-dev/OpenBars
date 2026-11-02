package wgkey

import (
	"encoding/base64"
	"encoding/hex"
	"testing"
)

func TestPublicMatchesRFC7748(t *testing.T) {
	priv, _ := hex.DecodeString("77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a")
	pub, err := Public(base64.StdEncoding.EncodeToString(priv))
	if err != nil {
		t.Fatal(err)
	}
	h, _ := Hex(pub)
	if h != "8520f0098930a754748b7ddcb43ef75a0dbf3a0d26381af4eba4a98eaa9b4e6a" {
		t.Fatalf("public = %s", h)
	}
}

func TestGenerate(t *testing.T) {
	k, err := Generate()
	if err != nil || !Valid(k) {
		t.Fatalf("key %q, %v", k, err)
	}
	b, _ := base64.StdEncoding.DecodeString(k)
	if b[0]&7 != 0 || b[31]&128 != 0 || b[31]&64 == 0 {
		t.Fatalf("not clamped: %x", b)
	}
	if _, err := Public(k); err != nil {
		t.Fatal(err)
	}
	if Valid("short") || Short(k) != k[:8]+"…" {
		t.Fatal("Valid/Short")
	}
}
