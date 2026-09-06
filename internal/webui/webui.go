// Package webui serves the browser UI. Assets are embedded so the release
// binary, Docker image and GoReleaser archives work from any directory with
// no asset lookup (review #05). WEB_DIR optionally overrides with on-disk
// files for UI development without rebuilding.
package webui

import (
	"embed"
	"os"
	"path/filepath"
)

//go:embed index.html login.html
var fs embed.FS

func load(name string) []byte {
	if dir := os.Getenv("WEB_DIR"); dir != "" {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			return b
		}
	}
	b, err := fs.ReadFile(name)
	if err != nil {
		return []byte("<html><body><h3>OmniHook UI unavailable.</h3></body></html>")
	}
	return b
}

// Index is the inbox single-page UI.
func Index() []byte { return load("index.html") }

// Login is the access-token sign-in shell.
func Login() []byte { return load("login.html") }
