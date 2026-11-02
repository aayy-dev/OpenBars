package state

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// DPAPI seals data for the account the service runs as (LocalSystem), so
// only the service on this PC can read the key back.
type DPAPI struct{}

var entropy = []byte("Open Bars Connect private key")

func (DPAPI) Seal(b []byte) ([]byte, error) {
	return crypt(b, true)
}

func (DPAPI) Open(b []byte) ([]byte, error) {
	return crypt(b, false)
}

func crypt(b []byte, seal bool) ([]byte, error) {
	if len(b) == 0 {
		return nil, nil
	}
	in := windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
	ent := windows.DataBlob{Size: uint32(len(entropy)), Data: &entropy[0]}
	var out windows.DataBlob
	var err error
	if seal {
		err = windows.CryptProtectData(&in, nil, &ent, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	} else {
		err = windows.CryptUnprotectData(&in, nil, &ent, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	}
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	res := make([]byte, out.Size)
	copy(res, unsafe.Slice(out.Data, out.Size))
	return res, nil
}
