"""Writes OpenBars/Resources/Licenses.json: the open-source code built into
Open Bars, shown in Settings › About › Open-Source Licenses.

usage: python3 tools/make_licenses.py   (needs Go and the modules in
core/go.mod downloaded, as for gomobile bind)

Lists every Go module linked into the WGCore and WGClient frameworks for iOS
and macOS, plus Go itself, with each one's license text. Run it again after
core/go.mod changes.
"""
import collections
import json
import os
import re
import subprocess
import sys

TOOLS = os.path.dirname(os.path.abspath(__file__))
CORE = os.path.join(TOOLS, "..", "core")
OUT = os.path.join(TOOLS, "..", "OpenBars", "Resources", "Licenses.json")

# What ends up in the frameworks: the two bound packages, plus the gomobile
# glue that gobind's generated code imports.
PACKAGES = ["./wgcore", "./wgclient", "golang.org/x/mobile/bind/seq"]
TARGETS = [("ios", "arm64"), ("darwin", "arm64")]

NAMES = {
    "golang.zx2c4.com/wireguard": "wireguard-go",
    "gvisor.dev/gvisor": "gVisor",
    "github.com/google/btree": "btree",
}
URLS = {
    "golang.zx2c4.com/wireguard": "https://git.zx2c4.com/wireguard-go",
    "gvisor.dev/gvisor": "https://gvisor.dev",
}
# Listed first, in this order; the rest follow alphabetically.
ORDER = ["golang.zx2c4.com/wireguard", "gvisor.dev/gvisor", "go"]

LICENSE_FILES = ["LICENSE", "LICENSE.md", "LICENSE.txt", "COPYING"]
# A real copyright line, not the Apache appendix's "Copyright [yyyy] [name]".
COPYRIGHT = re.compile(r"Copyright (\([cC]\) |©\s*)?(\d{4}|The )")


def go(*args, env=None):
    return subprocess.run(["go", *args], cwd=CORE, env=env, check=True,
                          capture_output=True, text=True).stdout.strip()


def linked_modules():
    found = {}
    fmt = ("{{if not .Standard}}{{with .Module}}{{if not .Main}}"
           "{{.Path}}\t{{.Version}}\t{{.Dir}}{{end}}{{end}}{{end}}")
    for goos, goarch in TARGETS:
        env = dict(os.environ, GOOS=goos, GOARCH=goarch, CGO_ENABLED="1")
        for line in go("list", "-deps", "-f", fmt, *PACKAGES, env=env).splitlines():
            if line:
                path, version, directory = line.split("\t")
                found[path] = (version, directory)
    return found


def license_text(directory):
    for name in LICENSE_FILES:
        path = os.path.join(directory, name)
        if os.path.exists(path):
            with open(path, encoding="utf-8") as f:
                return f.read().strip()
    sys.exit(f"no license file in {directory}")


def source_copyright(directory):
    """The most common copyright line in the module's .go file headers, for
    licenses (like wireguard-go's) that don't name the copyright holder."""
    seen = collections.Counter()
    for root, dirs, files in os.walk(directory):
        dirs.sort()
        for name in sorted(files):
            if not name.endswith(".go"):
                continue
            with open(os.path.join(root, name), encoding="utf-8", errors="ignore") as f:
                head = f.read(600)
            for line in head.splitlines():
                line = line.strip(" /*\t")
                if COPYRIGHT.match(line):
                    seen[line] += 1
                    break
    return seen.most_common(1)[0][0] if seen else None


def kind(text):
    if "Apache License" in text and "Version 2.0" in text:
        return "Apache-2.0"
    if "Permission is hereby granted, free of charge" in text:
        return "MIT"
    if "Redistribution and use in source and binary forms" in text:
        return "BSD-3-Clause" if "Neither the name" in text else "BSD-2-Clause"
    sys.exit("unrecognized license; add it to kind()")


def component(module, name, version, url, directory):
    text = license_text(directory)
    has_holder = any(COPYRIGHT.match(l.strip()) for l in text.splitlines())
    return {
        "name": name,
        "module": module,
        "version": version or None,
        "license": kind(text),
        "copyright": None if has_holder else source_copyright(directory),
        "url": url,
        "text": text,
    }


def main():
    goroot = go("env", "GOROOT")
    # Homebrew's Go keeps LICENSE one level above GOROOT.
    go_dir = next(d for d in (goroot, os.path.dirname(goroot))
                  if os.path.exists(os.path.join(d, "LICENSE")))
    items = [component("go", "Go", None, "https://go.dev", go_dir)]
    for module, (version, directory) in linked_modules().items():
        name = NAMES.get(module, module.removeprefix("golang.org/"))
        url = URLS.get(module, f"https://pkg.go.dev/{module}")
        items.append(component(module, name, version, url, directory))

    def rank(item):
        m = item["module"]
        return (ORDER.index(m) if m in ORDER else len(ORDER), item["name"].lower())

    items.sort(key=rank)
    with open(OUT, "w", encoding="utf-8") as f:
        json.dump({"components": items}, f, ensure_ascii=False, indent=1)
        f.write("\n")
    for item in items:
        print(f'{item["name"]:16} {item["license"]:13} {item["version"] or ""}')
    print(f"wrote {os.path.relpath(OUT)}")


main()
