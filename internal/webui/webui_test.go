package webui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedAssets(t *testing.T) {
	if b := Index(); !strings.Contains(string(b), "OmniHook") {
		t.Fatal("index.html missing")
	}
	if b := Login(); !strings.Contains(string(b), "/api/login") {
		t.Fatal("login.html missing")
	}
}

func TestWebDirOverride(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("OVERRIDE"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WEB_DIR", dir)
	if string(Index()) != "OVERRIDE" {
		t.Fatal("WEB_DIR override ignored")
	}
	// login.html absent on disk falls back to embedded.
	if b := Login(); !strings.Contains(string(b), "/api/login") {
		t.Fatal("embedded fallback broken")
	}
}
