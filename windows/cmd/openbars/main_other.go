//go:build !windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "Open Bars Connect for Windows only runs on Windows; build it with GOOS=windows (see windows/build.sh).")
	os.Exit(1)
}
