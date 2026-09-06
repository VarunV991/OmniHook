# Changelog

All notable changes to OmniHook. Format follows Keep a Changelog; versions follow SemVer.
`main` holds releases; `develop` holds unreleased work.

## [Unreleased] (develop)

Added:
- C fidelity: Stripe multi-signature accept-any; provider enum validation +
  `auto`; effective provider persisted (`verified_by`, migration 002) and used
  for re-sign, with explicit re-sign errors; canonical GC timestamps + boundary
  tests; shared outbound policy (no redirect following, hop-by-hop stripping);
  raw body export (API `/body`, `body_base64`, CLI `--raw`); replay 404/400
  validation, 5-min batch deadline, empty-body overrides; explicit-fields-only
  upsert (API + CLI); canonical header merge; oversized bodies 413-rejected.
- B installable UI + setup UX: `internal/webui` embeds inbox + login pages in
  the binary (plus `WEB_DIR` dev override); endpoint create form
  (provider/secret/target), copyable capture URLs, replay target prefill,
  verify reasons, delivery-attempt history API + UI panel; Docker data-volume
  ownership, host connectivity docs, CI Compose smoke (health→create→capture).
- A3 bounded delivery + shutdown: forward worker pool (8 workers, 128 queue;
  drops recorded, never silent); self-target refusal + marked-request loop
  breaker; http.Server timeouts with SIGINT/SIGTERM graceful shutdown
  (drains forwards, stops GC, closes DB last).
- A2 exposure lockdown: loopback bind by default (`BIND`, `--bind`), stderr
  warning for untokened external binds; public `/login` shell + `/api/login`
  HttpOnly cookie sessions + `/api/logout`; root serves login instead of 401.
- A1 trust fixes: capture returns 503 (never phantom success) on storage
  failure; 413 reject for oversized bodies (no truncated verify/forward);
  400 on unreadable bodies and invalid slugs; bounded management JSON
  (malformed/trailing rejected); PATCH status 200–599; startup config
  validation; health 503 when DB down; generic 500s (no DB detail leaks);
  shared `internal/slug` package.

## [v0.2.0] - 2026-09-06

Added:
- Review hardening: SSE fan-out Hub (every tab gets every event), UI XSS
  escaping + token prompt + error surfaces, endpoint GET/PATCH/DELETE and
  request DELETE APIs, `new` upsert (CLI + API), slug charset validation
  (shared `api.ValidSlug`), unknown CLI flags fail, `up` rejects positionals,
  untagged builds report `dev` version, replay records SSRF blocks and sends
  single Content-Type, forward skips FK-violating records, checkdocs
  schema-sync gate (migrations ↔ inline schema).
- GC + rate limiting (closes #4): `internal/gc` shared by one-shot `omnihook gc`
  and the server's hourly scheduler; `RATE_LIMIT_RPS` (default 50, 0 disables)
  token-bucket per IP on capture — over-limit requests are still stored as
  evidence but answered `429 + Retry-After: 1` so providers back off.
- Replay upgrades (closes #5): `times` (1–50) + `delay_ms` multi-replay with
  per-attempt results (API array, CLI `[i/N]` lines, UI times input);
  `resign` refreshes Stripe/GitHub/Standard/Razorpay/Shopify signatures with
  the endpoint secret so stale captures verify PASS (API flag, CLI `--resign`,
  UI checkbox); replay restores original headers (byte-faithful) with
  resigned/explicit overrides winning.
- Shopify verifier (`X-Shopify-Hmac-Sha256` base64 HMAC + topic/domain detect,
  incl. hex-instead-of-base64 hint) and Clerk compatibility proven via Standard
  `svix-*` headers; matrix now 29/29; `docs/PROVIDERS.md` §§5–6 (closes #6).
- CLI (closes #3): `new/list/show/replay/verify/gc`, `up --port`; flags accepted
  before or after positionals; exit codes 0 ok / 1 error / 2 verify-FAIL.
  `verify` is fully offline (headers/body files, `@path` or stdin).
- Provider test matrix: 21-case `TestProviderMatrix` (valid, auto-detect,
  tampered/wrong-secret/expired/missing-secret across Stripe, GitHub, Standard,
  Razorpay) + `docs/PROVIDERS.md` (connect guides, samples, live E2E results).
- Forward worker (`internal/forward`): endpoints with `target_url` auto-forward
  captured requests (method, original headers, raw bytes) async with 10s timeout;
  `X-Omnihook-Forward` + `X-Omnihook-Request-Id` headers; outcome recorded in
  `replays`; provider response never fails because forwarding failed; SSRF guard
  blocks cloud metadata hosts. Regression tests: success/bytes/headers recorded,
  broken target still captures 200.
- Docs-freshness automation: `scripts/checkdocs` (CHANGELOG + README env-table gates),
  `make verify` regression gate, PR template checklist; CI runs `make verify`.

## [v0.1.0] - 2026-09-06
First runnable release. Single Go binary + SQLite + embedded inbox UI (MIT).

Added:
- Capture `ALL /hook/:slug/*` with raw-byte preservation (1 MB cap, truncation flag), auto-create endpoint on first hit.
- Verifiers with actionable fix hints: Stripe (HMAC-SHA256, 5-min tolerance), GitHub (`X-Hub-Signature-256`), Standard Webhooks (`Webhook-Id/Timestamp/Signature`, `whsec_` base64), Razorpay, Generic HMAC fallback; `SKIPPED` when no provider matches.
- Live inbox UI (SSE) + management API: endpoints CRUD, request list/detail, replay with edit, `/health`.
- Replay client with SSRF guard (blocks cloud metadata hosts), 15s timeout, persisted `status_code/latency_ms`.
- Config via env: `PORT, DATA_DIR, RETENTION_HOURS, MAX_BODY_BYTES, ACCESS_TOKEN, PUBLIC_URL`.
- Packaging: multi-stage Dockerfile (distroless nonroot), `docker-compose.yml`, GoReleaser matrix, Makefile.
- Tests: verifier golden vectors (incl. raw-vs-pretty JSON mismatch), SSRF blocklist, hermetic API loop test (create→capture→list→detail→replay), `go vet` clean.
- Docs: `PLAN.md` (requirements/scope/phases/test/launch), README quickstart, `AGENTS.md`.
