# Changelog

All notable changes to OmniHook. Format follows Keep a Changelog; versions follow SemVer.
`main` holds releases; `develop` holds unreleased work.

## [Unreleased] (develop)

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
