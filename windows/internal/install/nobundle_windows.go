//go:build !bundle

package install

import "io/fs"

// Without -tags bundle the files must be next to OpenBars.exe.
var bundled fs.FS = noFiles{}

type noFiles struct{}

func (noFiles) Open(name string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}
