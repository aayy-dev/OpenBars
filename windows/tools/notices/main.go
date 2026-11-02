// notices writes THIRD_PARTY_NOTICES.txt for the Windows build: every Go
// module linked into OpenBars.exe (for GOOS=windows), Go itself, and Wintun,
// each with its license text.
//
// usage (from windows/): go run ./tools/notices OUT_FILE WINTUN_LICENSE_FILE
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type module struct {
	Path, Version, Dir string
	Main               bool
}

func main() {
	if len(os.Args) != 3 {
		log.Fatal("usage: notices OUT_FILE WINTUN_LICENSE_FILE")
	}
	cmd := exec.Command("go", "list", "-deps", "-json=Module", "./cmd/openbars")
	cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0")
	out, err := cmd.Output()
	if err != nil {
		log.Fatal(err)
	}
	mods := map[string]module{}
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var p struct{ Module *module }
		if err := dec.Decode(&p); err != nil {
			log.Fatal(err)
		}
		if p.Module != nil && !p.Module.Main {
			mods[p.Module.Path] = *p.Module
		}
	}
	var paths []string
	for p := range mods {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var b strings.Builder
	b.WriteString("Open Bars Connect for Windows includes the following open-source software.\n\n")
	b.WriteString("\"WireGuard\" and the \"WireGuard\" logo are registered trademarks of Jason A. Donenfeld.\n\n")

	goroot := strings.TrimSpace(run("go", "env", "GOROOT"))
	section(&b, "Go (standard library and runtime)", "https://go.dev", licenseIn(goroot, filepath.Dir(goroot)))
	for _, p := range paths {
		m := mods[p]
		section(&b, m.Path+" "+m.Version, "https://pkg.go.dev/"+m.Path, licenseIn(m.Dir))
	}
	wintun, err := os.ReadFile(os.Args[2])
	if err != nil {
		log.Fatal(err)
	}
	section(&b, "Wintun (wintun.dll, prebuilt and signed by WireGuard LLC)", "https://www.wintun.net", string(wintun))
	if err := os.WriteFile(os.Args[1], []byte(strings.ReplaceAll(b.String(), "\n", "\r\n")), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wrote %s (%d modules)\n", os.Args[1], len(paths))
}

func section(b *strings.Builder, title, url, text string) {
	fmt.Fprintf(b, "%s\n%s\n%s\n\n%s\n\n", strings.Repeat("=", 78), title, url, strings.TrimSpace(text))
}

func licenseIn(dirs ...string) string {
	for _, d := range dirs {
		for _, n := range []string{"LICENSE", "LICENSE.md", "LICENSE.txt", "COPYING"} {
			if b, err := os.ReadFile(filepath.Join(d, n)); err == nil {
				return string(b)
			}
		}
	}
	log.Fatalf("no license file in %v", dirs)
	return ""
}

func run(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		log.Fatal(err)
	}
	return string(out)
}
