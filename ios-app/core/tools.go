//go:build tools

// Package tools is never compiled. It only keeps golang.org/x/mobile in
// go.mod and go.sum: `gomobile bind` needs golang.org/x/mobile/bind to
// resolve from this module, and CI installs gomobile/gobind at the version
// recorded here.
package tools

import _ "golang.org/x/mobile/bind"
