// Command checkdocs enforces docs-freshness gates so README/CHANGELOG can't
// silently drift from code. Run: go run ./scripts/checkdocs
//
// Gate 1 (hard): if Go/web source changed vs main, CHANGELOG.md [Unreleased]
// section must contain entries.
// Gate 2 (hard): README config table must list every env key read in
// internal/config/config.go (getenv("X", ...) calls).
package main

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "checkdocs FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("checkdocs OK")
}

func run() error {
	changed := changedFiles()
	codeChanged := false
	for _, f := range changed {
		if strings.HasSuffix(f, ".go") || strings.HasPrefix(f, "web/") {
			if strings.HasPrefix(f, "scripts/checkdocs/") {
				continue
			}
			codeChanged = true
			break
		}
	}
	changelogTouched := false
	for _, f := range changed {
		if f == "CHANGELOG.md" {
			changelogTouched = true
		}
	}
	if codeChanged && !changelogTouched {
		if err := checkUnreleasedHasEntries(); err != nil {
			return fmt.Errorf("code changed but CHANGELOG.md not updated: %v", err)
		}
		fmt.Println("note: code changed without CHANGELOG touch, but [Unreleased] has entries")
	}
	if err := checkConfigTable(); err != nil {
		return err
	}
	return checkSchemaSync()
}

// changedFiles lists paths changed vs main (CI/PR) or working tree (local).
func changedFiles() []string {
	for _, args := range [][]string{
		{"diff", "--name-only", "main...HEAD"},
		{"diff", "--name-only", "origin/main...HEAD"},
	} {
		if out, err := exec.Command("git", args...).Output(); err == nil && len(out) > 0 {
			return splitLines(string(out))
		}
	}
	out, err := exec.Command("git", "status", "--porcelain").Output()
	if err != nil {
		return nil
	}
	var files []string
	for _, line := range splitLines(string(out)) {
		if len(line) > 3 {
			files = append(files, strings.TrimSpace(line[3:]))
		}
	}
	return files
}

func splitLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// checkUnreleasedHasEntries ensures ## [Unreleased] has bullet content.
func checkUnreleasedHasEntries() error {
	b, err := os.ReadFile("CHANGELOG.md")
	if err != nil {
		return err
	}
	lines := strings.Split(string(b), "\n")
	inUnreleased := false
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "## [Unreleased]") {
			inUnreleased = true
			continue
		}
		if inUnreleased && strings.HasPrefix(t, "## [") {
			break
		}
		if inUnreleased && (strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ")) {
			return nil
		}
	}
	return fmt.Errorf("CHANGELOG.md [Unreleased] section has no entries")
}

// checkConfigTable ensures every getenv("KEY") in config.go appears in README.
var getenvRe = regexp.MustCompile(`getenv\w*\(\s*"([A-Z_]+)"`)

func checkConfigTable() error {
	cfg, err := os.ReadFile("internal/config/config.go")
	if err != nil {
		return err
	}
	readme, err := os.ReadFile("README.md")
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, m := range getenvRe.FindAllSubmatch(cfg, -1) {
		seen[string(m[1])] = true
	}
	var missing []string
	for k := range seen {
		if !strings.Contains(string(readme), "`"+k+"`") {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("README.md missing config keys: %s", strings.Join(missing, ", "))
	}
	return nil
}

var wsRe = regexp.MustCompile(`\s+`)

// checkSchemaSync ensures every statement in migrations/001_init.sql has an
// identical twin in the inline schema const in internal/db/db.go (kept inline
// because go:embed cannot reference ../../ paths). Prevents silent drift.
func checkSchemaSync() error {
	mig, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		return err
	}
	dbsrc, err := os.ReadFile("internal/db/db.go")
	if err != nil {
		return err
	}
	norm := func(s string) string {
		// Strip -- comments FIRST (they may contain semicolons), then split.
		var code []string
		for _, l := range strings.Split(s, "\n") {
			if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "--") {
				code = append(code, t)
			}
		}
		var stmts []string
		for _, part := range strings.Split(strings.Join(code, "\n"), ";") {
			if t := wsRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(part)), " "); t != "" {
				stmts = append(stmts, t)
			}
		}
		return strings.Join(stmts, ";\n")
	}
	want, got := norm(string(mig)), norm(string(dbsrc))
	for _, stmt := range strings.Split(want, ";\n") {
		if stmt == "" {
			continue
		}
		if !strings.Contains(got, stmt) {
			return fmt.Errorf("migrations/001_init.sql statement missing from internal/db/db.go inline schema: %.80q...", stmt)
		}
	}
	return nil
}
