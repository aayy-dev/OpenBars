package engine

import (
	"encoding/base64"
	"encoding/hex"
	"testing"
)

func TestKeys(t *testing.T) {
	// RFC 7748 section 6.1 (Alice).
	priv, _ := hex.DecodeString("77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a")
	wantPub, _ := hex.DecodeString("8520f0098930a754748b7ddcb43ef75a0dbf3a0d26381af4eba4a98eaa9b4e6a")
	pub, err := PublicKey(" " + base64.StdEncoding.EncodeToString(priv) + "\n")
	if err != nil || pub != base64.StdEncoding.EncodeToString(wantPub) {
		t.Fatalf("RFC 7748 vector: %q %v", pub, err)
	}
	for _, bad := range []string{"", "abc", base64.StdEncoding.EncodeToString(make([]byte, 31)), "!!!!"} {
		if _, err := PublicKey(bad); err == nil {
			t.Errorf("PublicKey(%q) should fail", bad)
		}
	}
	for i := 0; i < 20; i++ {
		k, err := GenerateKey()
		if err != nil || len(k) != 44 {
			t.Fatalf("%q %v", k, err)
		}
		b, _ := base64.StdEncoding.DecodeString(k)
		if b[0]&7 != 0 || b[31]&0x80 != 0 || b[31]&0x40 == 0 {
			t.Fatalf("not clamped: %x", b)
		}
		if _, err := PublicKey(k); err != nil {
			t.Fatal(err)
		}
	}
	h, err := keyToHex(base64.StdEncoding.EncodeToString(priv))
	if err != nil || h != hex.EncodeToString(priv) || hexToKey(h) != base64.StdEncoding.EncodeToString(priv) {
		t.Fatalf("hex round trip %q %v", h, err)
	}
	if hexToKey("zz") != "" || hexToKey("abcd") != "" {
		t.Fatal("bad hex should give empty")
	}
}
