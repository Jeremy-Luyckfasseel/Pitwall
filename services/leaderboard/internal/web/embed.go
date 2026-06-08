package web

import (
	"embed"
	"io/fs"
)

// distFS holds the built Vite/React SPA. The bundle is produced by the Vite build
// (services/leaderboard/web → outDir internal/web/dist) and is embedded into the
// Go binary. In a Go-only build (and CI's leaderboard-go job) only the committed
// dist/.gitkeep is present; the Docker image's node stage populates the real
// bundle before the go build. "all:" so the .gitkeep placeholder is embedded and
// the directive always compiles.
//
//go:embed all:dist
var distFS embed.FS

// bundleFS returns the dist subtree as a filesystem rooted at the bundle, and
// whether a real index.html is present (i.e. the SPA was actually built).
func bundleFS() (fs.FS, bool) {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return sub, false
	}
	return sub, true
}
