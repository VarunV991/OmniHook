// Command omnihook — local-first universal webhook inbox.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"

	"github.com/you/omnihook/internal/api"
	"github.com/you/omnihook/internal/capture"
	"github.com/you/omnihook/internal/cli"
	"github.com/you/omnihook/internal/config"
	"github.com/you/omnihook/internal/db"
)

var version = "v0.1.0"

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

func runServe(cfg config.Config) int {
	sqldb, err := db.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "db open:", err)
		return cli.ExitError
	}
	defer sqldb.Close()
	hub := capture.NewHub()
	cap := &capture.Handler{DB: sqldb, Cfg: cfg, Hub: hub}
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
