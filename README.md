# OmniHook — local-first universal webhook inbox (MIT)

Capture, verify, replay webhooks locally. No account. Data stays in your SQLite file.

> Status: `v0.1.0` — runnable single binary. See [PLAN.md](PLAN.md) for scope and [CHANGELOG.md](CHANGELOG.md) for releases.

## Why

Debugging webhooks today means tunnels with changing URLs, single-provider CLIs with synthetic events, silent HMAC failures from parsed-instead-of-raw bodies, and hosted inspectors that keep your payment payloads. OmniHook gives you a permanent local capture URL, tells you **why** a signature failed (with the framework fix), and replays the exact bytes to localhost as many times as you need — offline after capture.

**Non-goals:** not a production gateway (no retries/DLQ/FIFO/portal). For sending webhooks to your users, use Svix/Hookdeck Outpost. For local debugging, use OmniHook.

## Quickstart (60s)

```bash
go run ./cmd/omnihook up
# UI: http://localhost:8080/   Health: http://localhost:8080/health

# 1. create endpoint
curl -s -X POST localhost:8080/api/endpoints -H 'Content-Type: application/json' \
  -d '{"slug":"proj1","provider":"stripe"}'

# 2. point provider (or tunnel) at http://<host>:8080/hook/proj1, then:
curl -X POST localhost:8080/hook/proj1/order/123 -H 'Content-Type: application/json' \
  -d '{"event":"ping"}'

# 3. list + replay to your app
curl -s localhost:8080/api/endpoints/proj1/requests
curl -s -X POST localhost:8080/api/requests/<id>/replay -H 'Content-Type: application/json' \
  -d '{"target":"http://localhost:3000/webhooks/stripe"}'
```

Docker:

```bash
docker compose up --build   # UI on :8080, data in ./data
```

Public URL for real providers (bring your own tunnel, v1 has no hosted relay):

```bash
cloudflared tunnel --url http://localhost:8080
PUBLIC_URL=https://<you>.trycloudflare.com go run ./cmd/omnihook up
```

## CLI

Everything works offline against the local DB file — no server needed except `up`:

```bash
omnihook new stripe1 --provider stripe --secret whsec_... --target http://localhost:3000/hook
omnihook list
omnihook show <request-id>
omnihook replay <request-id> --target http://localhost:3000/hook --header X-Debug=1 --times 5 --resign
omnihook verify --provider stripe --secret whsec_... --headers @h.json --body @b.bin  # exit 0 PASS, 2 FAIL
omnihook gc --retention-hours 48
omnihook up --port 8080
```

Flags may come before or after the positional arg. Per-provider setup, payload
samples, and the verified test matrix: [docs/PROVIDERS.md](docs/PROVIDERS.md).

## Forwarding to localhost

Set `target_url` on an endpoint and every capture is forwarded async (10s timeout)
with original method, headers, and raw bytes, plus `X-Omnihook-Forward: true` and
`X-Omnihook-Request-Id`. The provider always gets your configured mock response —
forwarding can never break capture. Each attempt is recorded (status + latency);
inspect via the UI replay panel or the `replays` table.

```bash
curl -s -X POST localhost:8080/api/endpoints -H 'Content-Type: application/json' \
  -d '{"slug":"proj1","provider":"stripe","target_url":"http://localhost:3000/webhooks/stripe"}'
```

## Signature verification (the useful part)

Supported: **Stripe** (`Stripe-Signature`), **GitHub** (`X-Hub-Signature-256`), **Standard Webhooks** (`Webhook-Id/Timestamp/Signature` — Svix/OpenAI/Anthropic/Clerk/Resend shape), **Razorpay**, **Shopify** (`X-Shopify-Hmac-Sha256`, base64), **Generic HMAC**. Auto-detected from headers or pinned per endpoint. Every `FAIL` ships a fix hint:

- Express: `app.post('/hook', express.raw({type:'application/json'}))` — never `express.json()` before HMAC.
- Spring Boot: `@RequestBody byte[] raw` + `Mac.getInstance("HmacSHA256")`.
- FastAPI: `raw = await request.body()`.
- Django: `request.body` (never `request.POST`).

## Configuration

| Env | Default | Meaning |
|-----|---------|---------|
| `PORT` | `8080` | HTTP port (UI + API + capture) |
| `DATA_DIR` | `./data` | SQLite lives here (`omnihook.db`) unless `DATABASE_URL` is set |
| `DATABASE_URL` | unset | Full SQLite path; overrides `DATA_DIR/omnihook.db` when set |
| `RETENTION_HOURS` | `168` | GC window for old requests |
| `MAX_BODY_BYTES` | `1048576` | Bodies above this are truncated (flagged) |
| `ACCESS_TOKEN` | unset | Gates UI + `/api/*`; `/hook/*` stays public by design |
| `PUBLIC_URL` | unset | Base URL rendered in capture URLs behind a tunnel |

## Layout

```
cmd/omnihook        binary entry (up|version)
internal/config     env config
internal/db         SQLite open + migrate (WAL)
internal/verify     stripe|github|standard|razorpay|generic + chain
internal/capture    raw-body capture handler + SSE hub
internal/forward    async forward worker (records to replays, SSRF-guarded)
internal/replay     replay client with SSRF guard
internal/api        REST + SSE + UI server (+ hermetic tests)
web/                single-page inbox UI
migrations/         idempotent SQL (source of truth; mirrored inline in db.go)
```

## Development

```bash
go vet ./... && go test ./... && go build ./...
```

Branching: `main` = releases, `develop` = integration, `feat/*` for work. See [CONTRIBUTING.md](CONTRIBUTING.md).
