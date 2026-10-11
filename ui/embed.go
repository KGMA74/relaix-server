// Package ui is the operator dashboard, compiled into gatewayd.
//
// The sources next to this file are a Vite + React project; `make ui` builds
// them into dist/, and this package embeds whatever dist/ holds at compile
// time. A checkout that never ran the build still compiles — dist/ keeps a
// placeholder file for exactly that — and the server then answers /ui/ with a
// page saying how to build it, so `go build` and `go test` never need Node.
package ui

import (
	"embed"
	"io/fs"
)

// all: so the placeholder dotfile counts: a bare pattern skips files starting
// with "." and would leave nothing to embed in an unbuilt checkout.
//
//go:embed all:dist
var dist embed.FS

// FS returns the built dashboard, rooted so that "index.html" is the entry
// point.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// fs.Sub only fails on an invalid path, and "dist" is a constant.
		panic(err)
	}
	return sub
}
