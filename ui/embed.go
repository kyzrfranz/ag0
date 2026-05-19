package ui

import (
	"embed"
	"io/fs"
)

// Built UI assets baked into the binary. The Vite output lives at ui/dist/ and
// must exist at compile time — run `npm run build` in this directory first.
// The `all:` prefix opts dotfile/underscore assets in (PWA service worker etc).
//
//go:embed all:dist
var distFS embed.FS

// DistFS returns the embedded dist tree as a filesystem rooted at the dist/
// directory itself, so callers can serve it via http.FileServer without
// re-prefixing every request path.
func DistFS() (fs.FS, error) {
	return fs.Sub(distFS, "dist")
}