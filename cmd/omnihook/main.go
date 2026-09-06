// Command omnihook — local-first universal webhook inbox.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/you/omnihook/internal/api"
	"github.com/you/omnihook/internal/capture"
	"github.com/you/omnihook/internal/cli"
	"github.com/you/omnihook/internal/config"
	"github.com/you/omnihook/internal/db"
	"github.com/you/omnihook/internal/gc"
)

// version is the release tag, injected by GoReleaser ldflags (-X main.version).
// Default "dev" marks untagged local builds so they never masquerade as a release.
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	cfg := config.Load(version)
	if len(args) == 0 {
		args = []string{"up"}
	}
	switch args[0] {
	case "up", "serve":
		fs := flag.NewFlagSet("up", flag.ContinueOnError)
		port := fs.String("port", cfg.Port, "HTTP port")
		if err := fs.Parse(args[1:]); err != nil {
			return cli.ExitError
		}
		if fs.NArg() > 0 {
			fmt.Fprintln(os.Stderr, "usage: omnihook up [--port P]")
			return cli.ExitError
		}
		cfg.Port = *port
		return runServe(cfg)
	case "version", "--version", "-v":
		fmt.Println(cfg.Version)
		return cli.ExitOK
	case "verify":
		// Pure offline computation — no database needed.
		return cli.Run(nil, cfg, args, os.Stdout, os.Stderr)
	default:
		sqldb, err := db.Open(cfg.DBPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "db open:", err)
			return cli.ExitError
		}
		defer sqldb.Close()
		return cli.Run(sqldb, cfg, args, os.Stdout, os.Stderr)
	}
}

// scheduleGC enforces retention hourly for long-running servers.
// The one-shot `omnihook gc` command covers ephemeral runs.
func scheduleGC(sqldb *sql.DB, retentionHrs int) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for range t.C {
		if reqs, eps, err := gc.Run(sqldb, retentionHrs); err != nil {
			log.Printf("gc: %v", err)
		} else if reqs+eps > 0 {
			log.Printf("gc: deleted %d requests, %d endpoints", reqs, eps)
		}
	}
}

func runServe(cfg config.Config) int {
	sqldb, err := db.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "db open:", err)
		return cli.ExitError
	}
	defer sqldb.Close()
	hub := capture.NewHub()
	cap := capture.NewHandler(sqldb, cfg, hub)
	go scheduleGC(sqldb, cfg.RetentionHrs)
	srv := api.New(sqldb, cfg, cap)
	addr := ":" + cfg.Port
	fmt.Printf("OmniHook %s listening on http://localhost%s\nUI: http://localhost%s/\nHealth: http://localhost%s/health\nData: %s\n",
		cfg.Version, addr, addr, addr, cfg.DBPath)
	if err := http.ListenAndServe(addr, srv.Mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return cli.ExitError
	}
	return cli.ExitOK
}
