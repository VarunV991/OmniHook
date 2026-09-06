// Command omnihook — local-first universal webhook inbox.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
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
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "invalid config:", err)
		return cli.ExitError
	}
	if len(args) == 0 {
		args = []string{"up"}
	}
	switch args[0] {
	case "up", "serve":
		fs := flag.NewFlagSet("up", flag.ContinueOnError)
		port := fs.String("port", cfg.Port, "HTTP port")
		bind := fs.String("bind", cfg.Bind, "bind address (127.0.0.1 default; 0.0.0.0 exposes)")
		if err := fs.Parse(args[1:]); err != nil {
			return cli.ExitError
		}
		if fs.NArg() > 0 {
			fmt.Fprintln(os.Stderr, "usage: omnihook up [--port P] [--bind ADDR]")
			return cli.ExitError
		}
		cfg.Port, cfg.Bind = *port, *bind
		if err := cfg.Validate(); err != nil {
			fmt.Fprintln(os.Stderr, "invalid config:", err)
			return cli.ExitError
		}
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
func scheduleGC(ctx context.Context, sqldb *sql.DB, retentionHrs int) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if reqs, eps, err := gc.Run(sqldb, retentionHrs); err != nil {
				log.Printf("gc: %v", err)
			} else if reqs+eps > 0 {
				log.Printf("gc: deleted %d requests, %d endpoints", reqs, eps)
			}
		}
	}
}

func runServe(cfg config.Config) int {
	sqldb, err := db.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "db open:", err)
		return cli.ExitError
	}
	hub := capture.NewHub()
	cap := capture.NewHandler(sqldb, cfg, hub)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go scheduleGC(ctx, sqldb, cfg.RetentionHrs)
	srv := api.New(sqldb, cfg, cap)
	httpSrv := &http.Server{
		Addr:              cfg.Bind + ":" + cfg.Port,
		Handler:           srv.Mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	addr := cfg.Bind + ":" + cfg.Port
	fmt.Printf("OmniHook %s listening on http://%s\nUI: http://%s/\nHealth: http://%s/health\nData: %s\n",
		cfg.Version, addr, addr, addr, cfg.DBPath)
	if !cfg.Loopback() {
		if cfg.AccessToken == "" {
			fmt.Fprintln(os.Stderr, "WARNING: listening on non-loopback "+cfg.Bind+" WITHOUT ACCESS_TOKEN: UI, API and replay are exposed to the network. Set ACCESS_TOKEN.")
		} else {
			fmt.Fprintln(os.Stderr, "NOTE: listening on non-loopback "+cfg.Bind+"; management is gated by ACCESS_TOKEN, /hook/* stays public by design.")
		}
	}
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.ListenAndServe() }()
	select {
	case <-ctx.Done():
		fmt.Fprintln(os.Stderr, "shutting down...")
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
		// Drain bounded forwarding before closing storage, so acknowledged
		// captures are not lost between forward and record.
		cap.Forwarder.Stop(5 * time.Second)
		_ = sqldb.Close()
		return cli.ExitOK
	case err := <-errCh:
		fmt.Fprintln(os.Stderr, err)
		_ = sqldb.Close()
		return cli.ExitError
	}
}
