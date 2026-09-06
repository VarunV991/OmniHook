// Command omnihook — local-first universal webhook inbox.
package main

import (
	"fmt"
	"net/http"
	"os"

	"github.com/you/omnihook/internal/api"
	"github.com/you/omnihook/internal/capture"
	"github.com/you/omnihook/internal/config"
	"github.com/you/omnihook/internal/db"
)

var version = "v0.1.0"

func main() {
	cfg := config.Load(version)
	cmd := "up"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "up", "serve":
		runServe(cfg)
	case "version", "--version", "-v":
		fmt.Println(version)
	default:
		fmt.Fprintf(os.Stderr, "usage: omnihook [up|version]\n")
		os.Exit(2)
	}
}

func runServe(cfg config.Config) {
	sqldb, err := db.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "db open:", err)
		os.Exit(1)
	}
	defer func() {
		_ = sqldb.Close()
	}()
	hub := capture.NewHub()
	cap := &capture.Handler{DB: sqldb, Cfg: cfg, Hub: hub}
	srv := api.New(sqldb, cfg, cap)
	addr := ":" + cfg.Port
	fmt.Printf("OmniHook %s listening on http://localhost%s\nUI: http://localhost%s/\nHealth: http://localhost%s/health\nData: %s\n",
		cfg.Version, addr, addr, addr, cfg.DBPath)
	if err := http.ListenAndServe(addr, srv.Mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
