# OmniHook — Plan of Action (v1 / MVP)

Status: v0.2.0 SHIPPED 2026-09-06 — all P0 scope items (F1–F10) plus extras
(CLI, 6th/7th verifiers, re-sign, GC scheduler, rate limiting, review hardening)
are on `main`. Only Deferred items remain open. Phases below are the historical
build record; see CHANGELOG.md for what each release contained.
Owner: solo-dev | Stack: Go + SQLite + embedded UI | License: MIT
Repo name: `omnihook` | Binary: `omnihook` | Tagline: local-first universal webhook inbox — capture, verify, replay.

## 1. Overall requirement

### 1.1 Problem (one paragraph)
Backend developers integrating Stripe / GitHub / Razorpay / Clerk / Shopify / Standard Webhooks providers cannot debug webhooks locally without pain: localhost is not public, tunnels (ngrok) change URLs and have no replay/history, provider CLIs are single-provider and synthetic, signature verification fails silently due to raw-body parsing, and hosted inspectors (webhook.site / RequestBin) leak sensitive payloads to third parties and expire. Debugging becomes `deploy -> trigger real payment -> read logs -> repeat`, breakpoint debugging is impossible due to provider timeouts.

### 1.2 Goal
A single MIT-licensed Go binary that gives a permanent local capture URL, preserves raw bytes, verifies signatures for 5 providers with actionable fix hints, shows a live inbox UI, and replays any captured request to localhost (with edit) unlimited times — no account, works offline after capture, data stays in local SQLite.

### 1.3 Success criteria for v1
1. `omnihook up` in < 60s from `brew install / docker run / go install`, UI at `localhost:8080`.
2. Capture any HTTP method/path/content-type up to 1 MB, byte-identical replay.
3. Verify PASS/FAIL for: Stripe, GitHub, Standard Webhooks (Svix/OpenAI/Anthropic/Clerk/Resend shape), Razorpay, Generic HMAC. Each FAIL includes framework-specific fix hint (Express / Spring Boot / FastAPI / Django).
4. Replay any request to any `http://localhost:*` target with header/body edit, showing status + latency, 50x replay without re-triggering provider.
5. 100% local: SQLite file, no external DB/Redis, no account, offline replay works.
6. Validation: demo GIF posted, >20 meaningful comments/upvotes across r/stripe + r/selfhosted + r/golang, 50 GitHub stars in first 30 days.

### 1.4 Explicit non-goals (v1 will NOT do)
- No production gateway features: no durable retries with backoff workers, no DLQ automation, no FIFO, no throttling, no customer portal, no multi-tenant auth.
- No payload transformations / workflow engine (webhook.site Custom Actions territory).
- No hosted global relay service operated by us. v1 supports bring-your-own tunnel (cloudflared / Tailscale Funnel / ngrok) + self-host on VPS. Optional relay is v2.
- No K8s operator, no SSO, no SOC2/compliance claims.

## 2. In-scope items (MVP feature list)

| # | Feature | Description | Priority |
|---|---------|-------------|----------|
| F1 | Capture endpoint | `POST /hook/:slug` + `ALL /hook/:slug/*` — any method, any suffix, any content-type. Returns configurable response (default 200 `{ok:true}`). Capture URL stays public, UI can be gated. | P0 |
| F2 | Raw-body preservation | Read body as bytes before any parse, store BLOB + `body_size`, `truncated` flag if >1 MB. Hash logged for dedup display. | P0 |
| F3 | Live inbox UI | Two-pane: left = request list (SSE live, newest first), right = detail (pretty JSON if JSON, raw view, header table, query params, verify badge, replay button). Dark mode, mobile usable. Single embedded static bundle, no second port. | P0 |
| F4 | Verifier chain | Auto-detect provider by headers, verify, store `verify_status + verify_error + fix_hint`. See 3.1 for 5 providers. `omnihook verify --dry-run` CLI for offline debugging. | P0 |
| F5 | Replay | `POST /api/requests/:id/replay {target, edit_headers, edit_body}` — re-sends method+headers+body to target, records `status_code, latency_ms`. Unlimited. Edit before send. | P0 |
| F6 | Endpoint mgmt API | `POST /api/endpoints` (create slug), `GET /api/endpoints`, `PATCH /api/endpoints/:slug` (name, target_url, response_status/body/content-type), `DELETE`, list/clear requests, SSE stream, health check. | P0 |
| F7 | Forward mode | `omnihook forward --source /hook/proj1 --target http://localhost:3000/hook` — auto-forward on arrival AND store. For real-time like ngrok. | P0 |
| F8 | Config + retention | Env: `PORT, DATA_DIR, RETENTION_HOURS (168), MAX_BODY_BYTES (1MB), ACCESS_TOKEN, PUBLIC_URL`. Scheduled GC deletes old requests/endpoints. | P0 |
| F9 | Packaging | Single binary (GoReleaser: linux/darwin/windows amd64+arm64), `Dockerfile` non-root + volume, `docker-compose.yml` example, Homebrew formula instructions, `npm i -g` thin wrapper (optional v1.1). | P0 |
| F10 | Docs snippets | Per-provider verify setup for Express / Spring Boot / FastAPI / Django — copy-paste raw-body snippets. This is a growth feature. | P0 |
| Deferred | Mock response from OpenAPI, team auth, global relay, OTel metrics, idempotency detector | Moved to v1.1 / idea #2 (webhookbox library). | P1+ |

## 3. Functional requirements

### 3.1 Signature verifiers (detailed)
All verifiers operate on **raw bytes**, use constant-time compare, enforce tolerance where applicable.

1. **Stripe:** header `Stripe-Signature: t=<unix>,v1=<hex>`. Compute `HMAC_SHA256(secret, "<t>.<raw_body>")`. Tolerance 5 min. FAIL hints: `Stripe-Signature missing -> wrong URL/secret; No signatures found with payload -> you parsed JSON before verify. Express: express.raw({type:'application/json'}); Spring: @RequestBody byte[]; FastAPI: await request.body(); Django: request.body`.
2. **GitHub:** header `X-Hub-Signature-256: sha256=<hex>`. Compute `HMAC_SHA256(secret, raw_body)`. No timestamp. Hint on `X-Hub-Signature` (SHA1 legacy) -> upgrade.
3. **Standard Webhooks:** headers `Webhook-Id, Webhook-Timestamp, Webhook-Signature: v1,<base64> ...`. Signed content = `<id>.<timestamp>.<raw_body>`. Secret = base64-decode part after `whsec_`. Tolerance 5 min. Covers Svix/OpenAI/Anthropic/Clerk/Resend/Supabase shape.
4. **Razorpay:** header `X-Razorpay-Signature: <hex>`. `HMAC_SHA256(secret, raw_body)`.
5. **Generic HMAC:** endpoint config `{header_name, prefix, algorithm}` — e.g. `X-Acme-Sig: sha256=<hex>`. Fallback if no provider matched -> `verify_status=SKIPPED`.

Store: `verify_status ENUM(PASS,FAIL,SKIPPED,ERROR)`, `verify_error TEXT`, `fix_hint TEXT`.

### 3.2 Capture API contract
- `ALL /hook/:slug` and `ALL /hook/:slug/*` -> 1) lookup endpoint, 2) read raw body (limit), 3) run verifier chain, 4) insert request row, 5) broadcast SSE, 6) forward if configured (async, don't block response), 7) return configured mock response (default 200 JSON).
- Must handle: empty body, binary, `multipart/form-data` (store raw, don't parse for verify), `application/x-www-form-urlencoded`, chunked.
- Response time p95 < 20ms local (excluding forward).

### 3.3 Replay API contract
- Input: `{target_url (must be http(s)), method_override?, headers_override?, body_override_base64?}`. SSRF guard: block `169.254.169.254`, `metadata.google.internal`, loopback allowlist is the point so allow `localhost` but block cloud metadata. Timeout 15s.
- Must strip incoming `host/content-length` and recompute. Preserve original `content-type` unless overridden.
- Output: `{status_code, latency_ms, response_headers, response_body_truncated_64k}` + persist row in `replays`.

### 3.4 UI requirements
- No login for `/hook/*`. If `ACCESS_TOKEN` set, `/` + `/api/*` (except `/health`, `/hook/*`) require `Authorization: Bearer` or cookie. Simple, no user table in v1.
- SSE endpoint: `GET /api/endpoints/:slug/stream` with `Event: request` JSON. Reconnect resilient.
- Detail view shows: `curl -X ...` reproduce command, `omnihook verify --dry-run` command, fix_hint banner if FAIL.

### 3.5 CLI requirements
```
omnihook up [--port 8080 --data-dir ./data]
omnihook new <name> [--provider stripe|github|standard|razorpay|generic --secret ... --target http://localhost:3000/hook]
omnihook list
omnihook forward --slug <slug> --target <url>
omnihook replay <request-id> --target <url> [--edit-body @file.json]
omnihook verify --provider stripe --secret whsec_... --headers @h.json --body @b.bin
omnihook gc [--retention-hours 168]
omnihook version
```
All commands work offline except those needing a public URL (user supplies tunnel URL via `PUBLIC_URL`).

## 4. Non-functional requirements

| Category | Requirement | How verified |
|----------|-------------|--------------|
| Performance | 500 req/s capture on laptop (M1, 1 MB bodies excluded), p95 < 20ms, SSE fanout < 100ms, SQLite WAL mode | `k6` / Go bench in CI |
| Reliability | No lost writes on crash (SQLite sync NORMAL + WAL), GC never deletes < retention, forward failure never fails capture response | Kill-test + integration test |
| Security | Raw secrets never logged, only `last4` shown; `ACCESS_TOKEN` gates UI; SSRF blocklist; `MAX_BODY_BYTES` enforced before read; timing-safe compare; Docker non-root; `gosec` clean | `gosec`, `govulncheck`, manual review |
| Portability | Single static binary linux/darwin/windows amd64+arm64, `docker image < 25 MB` (multi-stage distroless/scratch + ca-certs) | GoReleaser CI matrix |
| Operability | One volume `./data`, one port, `/health` returns `{"ok":true,"db":"up","version":"..."}`, structured JSON logs to stdout | Compose smoke test |
| Maintainability | `gofmt + go vet` clean, 70%+ coverage on verifiers + capture/replay handlers, no ORM (stdlib `database/sql` + thin layer), migrations idempotent numbered SQL | CI gates |
| UX | `up` to first captured request < 60s following README, no account, offline replay works with tunnel down | Timed manual test |
| License/legal | MIT, no telemetry, no phone-home, SBOM via `syft` on release | Release checklist |

## 5. Code build-out plan (phases, solo-dev, ~3-4 weeks)

**Phase 0 — Scaffold (Day 1-2) [this session]**
- `go.mod (go 1.23)`, `cmd/omnihook/main.go`, `internal/{config,db,capture,verify,replay,forward,api,ui}`, `web/` embedded UI placeholder, `migrations/001_init.sql`, `Dockerfile`, `docker-compose.yml`, `.goreleaser.yml`, `Makefile`, `README.md`, `PLAN.md` (this file).
- Acceptance: `go build ./... && go test ./...` passes, `omnihook up` serves `/health` + placeholder UI.

**Phase 1 — Capture + Store + UI skeleton (Week 1)**
- `internal/db` SQLite open/migrate/query, `internal/capture` raw-body handler, `internal/api` endpoints CRUD + SSE hub, minimal `web/index.html` list/detail.
- Tests: raw-byte preservation (JSON with whitespace/unicode must be identical), binary, 1 MB truncation, SSE broadcast.
- Acceptance: `curl POST /hook/test` appears live in UI < 1s.

**Phase 2 — Verifiers (Week 2)**
- `internal/verify/{stripe,github,standard,razorpay,generic}.go` + `chain.go` + `hints.go` (framework snippets).
- Golden tests per provider using real test vectors (Stripe constructEvent vectors, Standard Webhooks spec vectors). Fuzz raw-body edge cases.
- Acceptance: `omnihook verify` PASS/FAIL matches provider SDKs, FAIL shows correct hint.

**Phase 3 — Replay + Forward (Week 2-3)**
- `internal/replay` client with SSRF guard + timeout + recording, `internal/forward` async worker, UI replay modal with edit.
- Tests: byte-identical replay, header recompute, metadata-IP blocked, latency recorded.
- Acceptance: capture Stripe event once, replay 50x to `localhost:3000` with breakpoints, all 200.

**Phase 4 — Hardening + Packaging (Week 3-4)**
- GC job, `ACCESS_TOKEN` gate, rate-limit capture per IP (simple token bucket), structured logs, `/health`, Dockerfile hardening, GoReleaser, Homebrew tap docs, Compose example with Spring Boot + FastAPI echo targets.
- Tests: `gosec`, `govulncheck`, k6 smoke, Compose e2e.
- Acceptance: fresh laptop -> README quickstart -> first replay < 5 min.

**Phase 5 — Docs + Launch (Week 4)**
- README with GIF, `docs/VERIFY-{stripe,github,standard}.md` with Spring/Express/FastAPI snippets, `docs/TUNNELS.md` (cloudflared/Tailscale/ngrok), CHANGELOG, CONTRIBUTING, issue templates, discussion prompts.
- Launch per section 7.

Branch strategy: `main` protected, `feat/*` short branches, squash merge, Conventional Commits, tags `v0.1.0` via GoReleaser.

## 6. Test and validation plan

### 6.1 Automated
- Unit: verifiers (golden vectors + negative: wrong secret, expired timestamp, tampered body, parsed-vs-raw JSON mismatch test), SSRF blocklist, truncation.
- Integration: `httptest` capture -> SQLite -> SSE -> replay to fake target; forward-on-capture; GC expiry; auth gate.
- E2E (Compose): real Stripe CLI `stripe trigger` -> OmniHook -> FastAPI echo app; GitHub sample push -> verify; edit-and-replay flow via API.
- Perf: `go test -bench`, `k6 run scripts/k6-capture.js` (500 rps target), `go vet`, `gofmt -l`, `gosec`, `govulncheck` in GitHub Actions matrix (ubuntu/macos/windows).
- Coverage gate: verifiers 90%+, handlers 70%+.

### 6.2 Manual validation checklist (must pass before v0.1.0 tag)
1. Fresh clone, `go run ./cmd/omnihook up`, create endpoint via UI, `curl` JSON + form + binary, all appear with correct pretty/raw views.
2. Real Stripe test: point Dashboard to tunnel URL, complete test checkout, see PASS, replay to local Spring/FastAPI handler with breakpoint, fix, replay again same ID.
3. Offline: kill tunnel, replay cached request -> still works.
4. Secrets: check logs contain no full secret, UI shows `whsec_...ab12` only.
5. Expired timestamp rejected, tampered body FAIL with correct hint.
6. `RETENTION_HOURS=1` GC deletes after 1h, keeps newer.

### 6.3 User validation (dogfood + beta)
- Dogfood with idea #2 echo apps (Spring Boot + FastAPI) for 1 week.
- 5 beta users: 2 Stripe, 1 GitHub, 1 Razorpay/Clerk, 1 Standard Webhooks. Ask: time-to-first-replay, was fix_hint correct, would you replace ngrok/webhook.site for dev.

## 7. Advertisement plan (Reddit + dev forums — no spam, value-first)

Principles: no cross-post blast day 1. One home post + help in comments for 2 weeks. Demo GIF > text. Disclose OSS + MIT. No affiliate links.

### 7.1 Assets to prepare BEFORE posting
- 30s GIF: `stripe trigger -> inbox appears -> FAIL->hint->fix -> Replay 200`. Plus 90s YouTube unlisted.
- README quickstart copy-paste (5 lines), `docker run` one-liner, `curl` reproduce examples.
- 3 code snippets ready to paste in comments: Express raw-body, Spring Boot `byte[]`, FastAPI `request.body()`.

### 7.2 Where + angle (sequence over 14 days)
1. **r/stripe (Day 1, home):** Title: `I got tired of re-triggering payments to debug webhooks, so I built a local inbox with one-click replay (OSS, Go)`. Body: pain (50 replays for 1 bug), GIF, what it does/doesn't do, ask: `What provider should I add next — Razorpay? Clerk?` Flair: `Integration Help`. Respond to every comment with snippet.
2. **r/selfhosted (Day 3):** Angle: privacy + single binary + SQLite, no account. Title: `Self-hosted webhook.site alternative — single Go binary, SQLite, no expiry (MIT)`. Include Compose + `PUBLIC_URL` + retention config. Ask for ARM testing.
3. **r/golang (Day 5):** Angle: technical. `Show me your SSE + SQLite patterns — I built a webhook debugger, looking for review on raw-body handling`. Post `internal/verify` code link, ask for critique. This gets contributors.
4. **r/webdev + r/Backend (Day 7-9):** Angle: tutorial, not promo. `How I debug Stripe webhooks locally without re-triggering payments (capture -> breakpoint -> replay)`. Link repo at end as "I packaged this".
5. **Dev.to + HN Show HN (Day 10-12):** Dev.to canonical tutorial with Spring + FastAPI snippets. HN: `Show HN: OmniHook – local-first webhook inbox with signature debugging` — post morning ET, stay 6h to answer.
6. **GitHub Topics + Discord:** Tag `webhooks, stripe, debugging, selfhosted, golang, developer-tools`. Share in Svix/Standard-Webhooks discussions, Hookdeck community (respectfully as dev-tool, not prod replacement), local JS/Java/Python Discords.

### 7.3 Comment-help playbook (ongoing)
- Search weekly: `webhook testing localhost`, `Stripe webhook signature fails`, `ngrok URL changes`. Answer with fix_hint + `omnihook verify --dry-run` example, not just link.
- Collect: provider requests -> public roadmap issue, upvote-driven. Ship Razorpay/Clerk/Shopify next based on votes.
- Metrics: stars, time-to-first-replay feedback, verifier FAIL accuracy, tunnel guide hits. Kill what doesn't get comments.

### 7.4 Anti-spam checklist
- Read each sub's promo rules, get mod OK for r/stripe if needed, no duplicate posts, no AI-slop comments, disclose author, no paid plan to push (there is none).

## 8. Risks + mitigations
- Crowded SEO -> mitigate by owning GitHub/Discord/Reddit help, not Google ads.
- Tunnel reliability -> mitigate by BYO tunnel docs, not building relay v1.
- Signature edge cases (Shopify HMAC, multi-sig rotation) -> mitigate by generic-HMAC fallback + dry-run tool + golden tests.
- Scope creep to prod gateway -> enforce non-goals, redirect to idea #2 / Svix/Outpost for prod.

## 9. Next step after this doc
Scaffold repo per Phase 0, then implement Phase 1. If scaffold `go build` + `/health` passes, proceed.
