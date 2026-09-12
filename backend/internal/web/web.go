// Package web embeds the built Angular SPA. The dist/ directory is populated by the
// frontend build stage of the Dockerfile; a placeholder index.html is committed so
// a bare `go build` still serves something.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the SPA file system rooted at dist/, or nil if no real build is
// present (only the placeholder / no index.html).
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil
	}
	return sub
}
