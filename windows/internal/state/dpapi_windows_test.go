package state

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestDPAPIRoundTrip(t *testing.T) {
	sealed, err := DPAPI{}.Seal([]byte("secret key"))
	if err != nil || bytes.Contains(sealed, []byte("secret key")) {
		t.Fatalf("seal: %v", err)
	}
	plain, err := DPAPI{}.Open(sealed)
	if err != nil || string(plain) != "secret key" {
		t.Fatalf("open: %q %v", plain, err)
	}
	path := filepath.Join(t.TempDir(), "state.json")
	if err := Save(path, State{PrivateKey: "k", AutoConnect: true}, DPAPI{}); err != nil {
		t.Fatal(err)
	}
	st, err := Load(path, DPAPI{})
	if err != nil || st.PrivateKey != "k" || !st.AutoConnect {
		t.Fatalf("load: %+v %v", st, err)
	}
}
