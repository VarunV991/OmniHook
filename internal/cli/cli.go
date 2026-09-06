// Package cli implements omnihook subcommands. All commands are offline-first:
// they operate on the local SQLite file (or pure computation for verify),
// never requiring a running server — except `up`, which serves.
//
// Exit codes: 0 ok (verify PASS included), 1 usage/runtime error, 2 verify FAIL.
package cli

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/you/omnihook/internal/config"
	"github.com/you/omnihook/internal/gc"
	"github.com/you/omnihook/internal/replay"
	"github.com/you/omnihook/internal/verify"
)

const (
	ExitOK    = 0
	ExitError = 1
	ExitFail  = 2
)

// Run dispatches args (without program name). db may be nil for commands that
// don't need it (version, verify); cfg supplies paths and defaults.
func Run(db *sql.DB, cfg config.Config, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return ExitError
	}
	switch args[0] {
	case "new":
		return cmdNew(db, cfg, args[1:], stdout, stderr)
	case "list":
		return cmdList(db, args[1:], stdout, stderr)
	case "show":
		return cmdShow(db, args[1:], stdout, stderr)
	case "replay":
		return cmdReplay(db, args[1:], stdout, stderr)
	case "verify":
		return cmdVerify(args[1:], stdout, stderr)
	case "gc":
		return cmdGC(db, cfg, args[1:], stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, cfg.Version)
		return ExitOK
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		usage(stderr)
		return ExitError
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `omnihook <command> [flags]

  new <slug> [--provider NAME --secret S --target URL]  create capture endpoint
  list [--json]                                          list endpoints
  show <request-id> [--json]                             show captured request
  replay <request-id> --target URL [--edit-body @file] [--header K=V]...
  verify --provider NAME --secret S --headers @h.json --body @b.bin
  gc [--retention-hours N]                               delete old requests
  up [--port P]                                          serve UI + API + capture
  version`)
}

// parseMixed parses flags anywhere in args (Go's flag package stops at the
// first positional, so `replay <id> --target X` would silently ignore the flag).
// Space-separated values (`--target X`) stay attached to their flag; boolean
// flags and `--k=v` forms work as usual. Returns the positional args.
func parseMixed(fs *flag.FlagSet, args []string) []string {
	var flags, pos []string
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && len(a) > 1 && a != "-" {
			name := strings.TrimLeft(a, "-")
			fname, fval, _ := strings.Cut(name, "=")
			takesValue := true
			if f := fs.Lookup(fname); f != nil {
				if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
					takesValue = false
				}
			}
			flags = append(flags, a)
			if fval == "" && takesValue && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				flags = append(flags, args[i])
			}
			i++
			continue
		}
		pos = append(pos, a)
		i++
	}
	_ = fs.Parse(flags)
	return pos
}

func needDB(db *sql.DB, stderr io.Writer) bool {
	if db == nil {
		fmt.Fprintln(stderr, "database not available")
		return false
	}
	return true
}

func cmdNew(db *sql.DB, cfg config.Config, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	fs.SetOutput(stderr)
	provider := fs.String("provider", "generic", "provider: stripe|github|standard|razorpay|shopify|generic")
	secret := fs.String("secret", "", "webhook signing secret")
	target := fs.String("target", "", "forward target URL (e.g. http://localhost:3000/hook)")
	pos := parseMixed(fs, args)
	if !needDB(db, stderr) {
		return ExitError
	}
	if len(pos) < 1 {
		fmt.Fprintln(stderr, "usage: omnihook new <slug> [--provider --secret --target]")
		return ExitError
	}
	slug := pos[0]
	if _, err := db.Exec(`INSERT INTO endpoints(slug,name,provider,secret_ref,target_url) VALUES(?,?,?,?,?)`,
		slug, slug, *provider, *secret, *target); err != nil {
		fmt.Fprintf(stderr, "create endpoint: %v\n", err)
		return ExitError
	}
	base := cfg.PublicURL
	if base == "" {
		base = "http://localhost:" + cfg.Port
	}
	fmt.Fprintf(stdout, "endpoint %q provider=%s\ncapture_url: %s/hook/%s\n", slug, *provider, base, slug)
	return ExitOK
}

func cmdList(db *sql.DB, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return ExitError
	}
	if !needDB(db, stderr) {
		return ExitError
	}
	rows, err := db.Query(`SELECT e.slug, e.provider, e.target_url, COUNT(r.id),
		COALESCE(MAX(r.received_at),'') FROM endpoints e LEFT JOIN requests r ON r.endpoint_slug=e.slug
		GROUP BY e.slug ORDER BY e.slug`)
	if err != nil {
		fmt.Fprintf(stderr, "list: %v\n", err)
		return ExitError
	}
	defer rows.Close()
	type row struct {
		Slug, Provider, Target, Last string
		Requests                     int
	}
	var out []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.Slug, &r.Provider, &r.Target, &r.Requests, &r.Last); err != nil {
			fmt.Fprintf(stderr, "list: %v\n", err)
			return ExitError
		}
		out = append(out, r)
	}
	if *asJSON {
		_ = json.NewEncoder(stdout).Encode(out)
		return ExitOK
	}
	if len(out) == 0 {
		fmt.Fprintln(stdout, "no endpoints (omnihook new <slug> to create one)")
		return ExitOK
	}
	fmt.Fprintln(stdout, "SLUG\tPROVIDER\tREQUESTS\tTARGET\tLAST")
	for _, r := range out {
		fmt.Fprintf(stdout, "%s\t%s\t%d\t%s\t%s\n", r.Slug, r.Provider, r.Requests, r.Target, r.Last)
	}
	return ExitOK
}

func cmdShow(db *sql.DB, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "full JSON output")
	pos := parseMixed(fs, args)
	if !needDB(db, stderr) {
		return ExitError
	}
	if len(pos) < 1 {
		fmt.Fprintln(stderr, "usage: omnihook show <request-id>")
		return ExitError
	}
	var method, path, query, headers, ctype, vs, verr, hint, at, slug string
	var body []byte
	var size, truncated int
	err := db.QueryRow(`SELECT endpoint_slug, method, path, query, headers, content_type,
		verify_status, verify_error, fix_hint, received_at, body, body_size, truncated
		FROM requests WHERE id=?`, pos[0]).
		Scan(&slug, &method, &path, &query, &headers, &ctype, &vs, &verr, &hint, &at, &body, &size, &truncated)
	if err != nil {
		fmt.Fprintf(stderr, "request not found: %s\n", pos[0])
		return ExitError
	}
	if *asJSON {
		_ = json.NewEncoder(stdout).Encode(map[string]any{
			"id": pos[0], "endpoint": slug, "method": method, "path": path,
			"query": query, "headers": json.RawMessage(headers), "content_type": ctype,
			"verify_status": vs, "verify_error": verr, "fix_hint": hint,
			"received_at": at, "body_size": size, "truncated": truncated,
			"body_text": string(body),
		})
		return ExitOK
	}
	where := path
	if query != "" {
		where += "?" + query
	}
	fmt.Fprintf(stdout, "%s %s  [%s]  endpoint=%s  %s\n", method, where, vs, slug, at)
	fmt.Fprintf(stdout, "content-type: %s  size: %d%s\n", ctype, size, map[bool]string{true: " (truncated)", false: ""}[truncated == 1])
	if verr != "" {
		fmt.Fprintf(stdout, "verify_error: %s\n", verr)
	}
	if hint != "" {
		fmt.Fprintf(stdout, "fix: %s\n", hint)
	}
	fmt.Fprintf(stdout, "--- body ---\n%s\n", body)
	return ExitOK
}

func cmdReplay(db *sql.DB, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	fs.SetOutput(stderr)
	target := fs.String("target", "", "destination URL (required)")
	editBody := fs.String("edit-body", "", "replace body with file contents (@path or - for stdin)")
	times := fs.Int("times", 1, "repeat replay N times (max 50)")
	delayMs := fs.Int("delay-ms", 0, "delay between repeats in ms")
	resign := fs.Bool("resign", false, "refresh time-sensitive signatures with endpoint secret so old captures verify PASS")
	var headers multiFlag
	fs.Var(&headers, "header", "override/add header K=V (repeatable)")
	pos := parseMixed(fs, args)
	if !needDB(db, stderr) {
		return ExitError
	}
	if len(pos) < 1 || *target == "" {
		fmt.Fprintln(stderr, "usage: omnihook replay <request-id> --target URL [--edit-body @file] [--header K=V]... [--times N] [--delay-ms MS] [--resign]")
		return ExitError
	}
	if *times < 1 || *times > 50 {
		fmt.Fprintln(stderr, "times must be 1..50")
		return ExitError
	}
	var bodyOverride []byte
	if *editBody != "" {
		b, err := readFileOrStdin(*editBody)
		if err != nil {
			fmt.Fprintf(stderr, "edit-body: %v\n", err)
			return ExitError
		}
		bodyOverride = b
	}
	overrides := map[string]string{}
	for _, h := range headers {
		k, v, ok := strings.Cut(h, "=")
		if !ok {
			fmt.Fprintf(stderr, "bad --header %q (need K=V)\n", h)
			return ExitError
		}
		overrides[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if *times == 1 && !*resign {
		code, lat, resp := replay.Send(db, pos[0], *target, overrides, bodyOverride)
		fmt.Fprintf(stdout, "status=%d latency_ms=%d\n%s\n", code, lat, truncate(resp, 2000))
		if code == 0 {
			return ExitError
		}
		return ExitOK
	}
	// Extended path: resign and/or repeats.
	opts := replay.Options{Headers: overrides, Body: bodyOverride, Resign: *resign}
	failed := false
	for i := 0; i < *times; i++ {
		if i > 0 && *delayMs > 0 {
			time.Sleep(time.Duration(*delayMs) * time.Millisecond)
		}
		c, l, _ := replay.SendWithOptions(db, pos[0], *target, opts)
		fmt.Fprintf(stdout, "[%d/%d] status=%d latency_ms=%d\n", i+1, *times, c, l)
		if c == 0 {
			failed = true
		}
	}
	if failed {
		return ExitError
	}
	return ExitOK
}

func cmdVerify(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	provider := fs.String("provider", "", "stripe|github|standard|razorpay|shopify|generic (required)")
	secret := fs.String("secret", "", "signing secret (required)")
	headersPath := fs.String("headers", "", "JSON headers file: @path or - for stdin (required)")
	bodyPath := fs.String("body", "", "raw body file: @path or - for stdin (required)")
	if err := fs.Parse(args); err != nil {
		return ExitError
	}
	if *provider == "" || *secret == "" || *headersPath == "" || *bodyPath == "" {
		fmt.Fprintln(stderr, "usage: omnihook verify --provider NAME --secret S --headers @h.json --body @b.bin")
		return ExitError
	}
	hb, err := readFileOrStdin(*headersPath)
	if err != nil {
		fmt.Fprintf(stderr, "headers: %v\n", err)
		return ExitError
	}
	var headers map[string]string
	if err := json.Unmarshal(hb, &headers); err != nil {
		fmt.Fprintf(stderr, "headers: not a JSON object: %v\n", err)
		return ExitError
	}
	body, err := readFileOrStdin(*bodyPath)
	if err != nil {
		fmt.Fprintf(stderr, "body: %v\n", err)
		return ExitError
	}
	res := verify.Chain(*secret, *provider, headers, body, time.Now())
	fmt.Fprintf(stdout, "provider=%s status=%s\n", res.Provider, res.Status)
	if res.Error != "" {
		fmt.Fprintf(stdout, "error: %s\n", res.Error)
	}
	if res.FixHint != "" {
		fmt.Fprintf(stdout, "fix: %s\n", res.FixHint)
	}
	if res.Status == verify.PASS {
		return ExitOK
	}
	return ExitFail
}

func cmdGC(db *sql.DB, cfg config.Config, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gc", flag.ContinueOnError)
	fs.SetOutput(stderr)
	retention := fs.Int("retention-hours", cfg.RetentionHrs, "delete requests older than N hours")
	if err := fs.Parse(args); err != nil {
		return ExitError
	}
	if !needDB(db, stderr) {
		return ExitError
	}
	reqs, eps, err := gc.Run(db, *retention)
	if err != nil {
		fmt.Fprintf(stderr, "gc: %v\n", err)
		return ExitError
	}
	fmt.Fprintf(stdout, "gc: deleted %d requests, %d expired endpoints (retention %dh)\n", reqs, eps, *retention)
	return ExitOK
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func readFileOrStdin(spec string) ([]byte, error) {
	p := strings.TrimPrefix(spec, "@")
	if spec == "-" || p == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(p)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…(truncated)"
	}
	return s
}
