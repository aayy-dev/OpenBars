// Package state is what the service remembers between runs: this PC's
// WireGuard key, the pairing with the iPhone and the auto-connect choice.
package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/aayy-dev/OpenBars/windows/internal/phone"
)

// Sealer protects the private key at rest (DPAPI on Windows).
type Sealer interface {
	Seal([]byte) ([]byte, error)
	Open([]byte) ([]byte, error)
}

// State is saved as JSON; the private key only ever in sealed form.
type State struct {
	PrivateKey  string         `json:"-"`
	SealedKey   []byte         `json:"private_key_sealed,omitempty"`
	Pairing     *phone.Pairing `json:"pairing,omitempty"`
	AutoConnect bool           `json:"auto_connect"`
}

// Paired reports whether this PC has an iPhone to connect to.
func (s *State) Paired() bool { return s.Pairing != nil && s.PrivateKey != "" }

// Load reads the state; a missing file is an empty state.
func Load(path string, sealer Sealer) (State, error) {
	var s State
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return State{AutoConnect: true}, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, err
	}
	if len(s.SealedKey) > 0 {
		k, err := sealer.Open(s.SealedKey)
		if err != nil {
			// Unreadable key (e.g. copied from another PC): start over with a new one.
			s.SealedKey, s.Pairing = nil, nil
			return s, errors.New("couldn't unlock the saved key; pair again")
		}
		s.PrivateKey = string(k)
	}
	return s, nil
}

// Save writes the state atomically.
func Save(path string, s State, sealer Sealer) error {
	if s.PrivateKey != "" {
		sealed, err := sealer.Seal([]byte(s.PrivateKey))
		if err != nil {
			return err
		}
		s.SealedKey = sealed
	} else {
		s.SealedKey = nil
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Plain stores keys unprotected; for tests and non-Windows builds.
type Plain struct{}

func (Plain) Seal(b []byte) ([]byte, error) { return b, nil }
func (Plain) Open(b []byte) ([]byte, error) { return b, nil }
