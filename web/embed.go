// Package web embeds the built single-page application (web/dist) into the
// binary. Run `make web` (or the container build) to populate dist/; without
// it the server serves a placeholder page.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the built UI rooted at dist/.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
