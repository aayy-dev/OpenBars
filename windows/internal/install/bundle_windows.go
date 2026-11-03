//go:build bundle

package install

import (
	"embed"
	"io/fs"
)

// Release builds (build.sh, -tags bundle) carry wintun.dll and the license
// files inside OpenBars.exe, so the download is a single file. build.sh
// stages them in bundle/ for the architecture being built; the installer
// writes them out unchanged next to the installed exe.
//
//go:embed bundle
var bundleFS embed.FS

var bundled = mustSub(bundleFS, "bundle")

func mustSub(f fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return sub
}
