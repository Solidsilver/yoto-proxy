// Package web embeds the built SvelteKit frontend (web/build).
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:build
var build embed.FS

// Build returns the static frontend rooted at the build directory.
func Build() fs.FS {
	sub, err := fs.Sub(build, "build")
	if err != nil {
		panic(err)
	}
	return sub
}
