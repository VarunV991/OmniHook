# AGENTS.md — instructions for AI coding agents working in this repo

Read this first. It saves you from the traps already discovered here.

## 1. What this is

OmniHook: local-first universal webhook inbox. Single Go binary + SQLite (WAL) + embedded
single-page UI. Captures `ALL /hook/:slug/*` preserving **raw bytes**, verifies HMAC
signatures (Stripe, GitHub, Standard Webhooks, Razorpay, Generic), serves a live SSE inbox,
and replays exact bytes to localhost. MIT. Full spec: `README.md` + `docs/PROVIDERS.md`.

## 2. Branching (strict develop → main)

- `main` = last stable release only. Never commit to it. Release flow only:
  `develop` → PR → merge → tag `vX.Y.Z` → `gh release create`.
- `develop` = integration. You work here (or in `feat/*` branched from it).
- Hotfix: branch `hotfix/*` from `main`, merge into **both** `main` and `develop`.
- Commits: Conventional Commits (`feat:`, `fix:`, `chore:`, `docs:`).

## 3. Build / test / run (Windows PowerShell 5.1 sandbox)

```powershell
go vet ./...; if ($?) { go test ./... }   # gate: must pass before push
go build -o bin/omnihook ./cmd/omnihook
go run ./cmd/omnihook up                  # foreground only (see §4)
```

- Toolchain: Go 1.22, module `github.com/you/omnihook` (rename when repo namespace is final).
- DB driver: `modernc.org/sqlite` (pure Go, no CGO). First `go mod tidy`/build downloads
  heavily — allow 300s timeouts.

## 4. Sandbox gotchas (learned the hard way)

These apply on every OS unless marked. When in doubt, prefer hermetic
`httptest` coverage (§4c.1) over shell-driven servers.

### 4a. Windows (PowerShell 5.1)

1. **Background processes do not survive a tool call.** The harness kills lingering child
   processes (`ChildProcess.kill`). `Start-Job` and detached `Start-Process` servers die
   with the call — that fake `HEALTH_NEVER_UP` cost a full debug cycle.
2. **To smoke-test the server, do everything in ONE bash call**: launch via
   `System.Diagnostics.Process` (`UseShellExecute=$false`, env vars set on
   `StartInfo.EnvironmentVariables` **before** `Start()`), poll `/health`, exercise the
   API, then `Kill()` in a `finally` block. `Start-Process` cmdlet is broken here —
   do not use it.
3. **Env must precede process start.** `$env:PORT=...` set after launch does
   nothing to the child. Always use a fresh port per run (e.g. `18081`) and a temp
   data dir (`Join-Path $env:TEMP "omnihook-smoke"`).
4. **Build with `.exe` suffix** (`go build -o bin\omnihook.exe ...`): the shell
   cannot execute or pipe extensionless binaries (`CantActivateDocumentInPipeline`).
5. **No `grep`/`&&`:** this shell has neither; use the `grep` tool and
   `; if ($?) { ... }` chaining. Quote with single quotes; avoid `\"` escapes —
   prefer string concatenation over interpolation when nesting quotes.

### 4b. Linux / macOS (bash)

1. Same single-call rule: start the server, poll `/health`, exercise, `kill` —
   all in one tool call. Prefer `./bin/omnihook & SRV=$!; ...; kill $SRV`, but
   be aware the harness may reap children when the call ends, so keep the whole
   loop inside the call and `kill` explicitly (trap on EXIT helps).
2. Env via `export PORT=... DATA_DIR=$(mktemp -d)` before launch; fresh port per run.
3. Binary has no extension (`go build -o bin/omnihook ...`); `grep`, `&&`, and
   double-quote interpolation all work normally.

### 4c. All shells

1. **Prefer hermetic tests.** `internal/api/server_test.go` runs the full
   create→capture→list→detail→replay loop in-process via `httptest` — no ports, no
   processes. Add regression coverage there, not in shell scripts.
2. File writes: use `read` before `edit`/`write`; keep `oldString` boundaries minimal.
3. Never use output truncation to limit tool results; full output is captured to
   a file — search it instead (`Select-Object -First` on Windows, `head` on bash).

## 5. Code conventions

- `gofmt` clean, `go vet` clean. No ORM — `database/sql` + numbered SQL files in
  `migrations/` (source of truth). `internal/db/db.go` mirrors the schema inline because
  `go:embed` cannot reference `../../` paths — keep both in sync when changing tables.
- Same `go:embed` restriction applies to `web/index.html`: it is served from disk with a
  placeholder fallback (`internal/api/indexHTML()`), not embedded.
- Verifiers (`internal/verify/`) operate on **raw body bytes**; verification must be
  constant-time (`secureEqual`). Every verifier needs golden PASS + tampered/expired
  negative tests. Every `FAIL` must include a `FixHint` with copy-paste snippets for
  Express / Spring Boot / FastAPI / Django.
- Capture handler must **never** fail the provider response because forwarding failed
  (forward is async/fire-and-forget). Respect `MAX_BODY_BYTES` before reading.
- Replay: SSRF blocklist (`internal/replay/blocked`) must keep blocking cloud metadata
  hosts while allowing `localhost`. 15s timeout. Record every attempt in `replays`.
- Secrets: never log full values; UI/API show suffix only. `/hook/*` is public by design;
  everything else honors `ACCESS_TOKEN` when set.

## 6. Docs to keep in sync with every feature PR

- `CHANGELOG.md` Unreleased section.
- `README.md` config table / quickstart / layout if flags, endpoints, env vars, or packages change.
- Scope tracking lives in GitHub issues (labels + milestone), not a plan doc.
- `docs/` guides (`PROVIDERS.md`, `MANUAL-TEST.md`, `LAUNCH.md`) when behavior they describe changes.

## 7. Release checklist (maintainer only, on `develop` when green)

1. `go vet` + `go test` + binary smoke (`health→create→capture→replay`, expect `SMOKE_PASS`).
2. Move `CHANGELOG.md` Unreleased → `vX.Y.Z` + date.
3. `gh pr create --base main --head develop`, `gh pr merge`, fetch, fast-forward local `main`,
   `git tag -a vX.Y.Z`, `git push origin main --tags`, `gh release create`.
4. `git checkout develop` when done — leave the tree on `develop`.
