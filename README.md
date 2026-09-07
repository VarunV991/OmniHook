# OmniHook — local-first universal webhook inbox (MIT)

Capture, verify, replay webhooks locally. No account. Data stays in your SQLite file.

> Latest tagged release: `v0.2.0`. This branch also contains unreleased review fixes; see [CHANGELOG.md](CHANGELOG.md). Start with [why it exists](docs/why.md), [architecture](docs/architecture.md), [a debugging session](docs/flow.md), or the [manual test guide](docs/MANUAL-TEST.md).

## Why

Debugging webhooks today means tunnels with changing URLs, single-provider CLIs with synthetic events, silent HMAC failures from parsed-instead-of-raw bodies, and hosted inspectors that keep your payment payloads. OmniHook gives you a stable local capture path (public tunnel URLs may change), reports signature failures with error-specific guidance, and replays the exact bytes to localhost as many times as you need — offline after capture.

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

Docker (UI/API on :8080, token `changeme` — change it):

```bash
docker compose up --build
```

Notes: data lives in a named volume (bind mounts need `chown 65532:65532`);
from inside Compose, your host app is `http://host.docker.internal:3000/...`
and the bundled echo target is `http://echo:3000`. UI developers can live-edit
via `WEB_DIR=<repo>/internal/webui`.

Public URL for real providers (bring your own tunnel, v1 has no hosted relay):

```bash
cloudflared tunnel --url http://localhost:8080
ACCESS_TOKEN=<choose-a-secret> PUBLIC_URL=https://<you>.trycloudflare.com go run ./cmd/omnihook up
```

## CLI

Commands use the local DB directly — no OmniHook server needed except `up`. Replay still needs network access to its target:

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
`X-Omnihook-Request-Id`. The target is the **complete replacement URL**: the
original subpath/query are not appended. Connection-scoped headers are stripped
and redirects are never followed (the 3xx is recorded instead). The provider
always gets your configured mock response —
forwarding can never break capture. Each attempt is recorded (status + latency);
inspect via the UI replay panel or the `replays` table. Delivery runs on a
bounded pool (8 workers, 128 queue); drops and self-target loops are recorded,
not silent. Marked requests (already forwarded/replayed by OmniHook) are
captured but never re-forwarded.

```bash
curl -s -X POST localhost:8080/api/endpoints -H 'Content-Type: application/json' \
  -d '{"slug":"proj1","provider":"stripe","target_url":"http://localhost:3000/webhooks/stripe"}'
```

### Endpoints, slugs, and limits

- Slugs match `^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$` (same rule in CLI, API, and capture).
- Full endpoint lifecycle: `GET/PATCH/DELETE /api/endpoints/:slug`,
  `DELETE /api/endpoints/:slug/requests`, `DELETE /api/requests/:id`.
  Re-running `omnihook new` (or `POST /api/endpoints`) updates only explicitly supplied fields; omitted values stay unchanged.
- Set `ACCESS_TOKEN` to gate the UI + API; sign in at `/login` (or `/api/login`),
  which sets an HttpOnly session cookie (12h). `/hook/*` stays public by design.

## Exposing via tunnel (read this before you forward a port)

OmniHook binds loopback (`127.0.0.1`) by default — nothing else on the network
can reach the UI, API, or replay. To receive real provider webhooks you have
two safe options:

1. **Tunnel to loopback (recommended):** `cloudflared tunnel --url http://localhost:8080`
   with `PUBLIC_URL=https://<you>.trycloudflare.com`. The server never listens
   externally; only the tunnel forwards to it.
2. **Bind externally:** `omnihook up --bind 0.0.0.0` (or `BIND=0.0.0.0`).
   Then `ACCESS_TOKEN` is **required hygiene** — the server prints a stderr
   WARNING without it. Management stays gated; `/hook/*` is public by design.

Never expose an untokened instance: stored payloads and replay can touch your
local services.

## Signature verification (the useful part)

Supported: **Stripe** (`Stripe-Signature`), **GitHub** (`X-Hub-Signature-256`), **Standard Webhooks** (`Webhook-Id/Timestamp/Signature` — Svix/OpenAI/Anthropic/Clerk/Resend shape), **Razorpay**, **Shopify** (`X-Shopify-Hmac-Sha256`, base64). Auto-detected from headers or pinned per endpoint. `generic` currently uses auto-detection and otherwise returns `SKIPPED`; custom HMAC header/prefix configuration is not exposed by the API or CLI. Failure messages distinguish secret, header, timestamp, and body problems. Raw-body handling examples:

- Express: `app.post('/hook', express.raw({type:'application/json'}))` — never `express.json()` before HMAC.
- Spring Boot: `@RequestBody byte[] raw` + `Mac.getInstance("HmacSHA256")`.
- FastAPI: `raw = await request.body()`.
- Django: `request.body` (never `request.POST`).

## Configuration

| Env | Default | Meaning |
|-----|---------|---------|
| `BIND` | `127.0.0.1` | Listen address. Loopback by default; set `0.0.0.0` deliberately to expose (see tunnel section) |
| `PORT` | `8080` | HTTP port (UI + API + capture) |
| `DATA_DIR` | `./data` | SQLite lives here (`omnihook.db`) unless `DATABASE_URL` is set |
| `DATABASE_URL` | unset | Full SQLite path; overrides `DATA_DIR/omnihook.db` when set |
| `RATE_LIMIT_RPS` | `50` | Capture responses per second per IP (`0` disables); over-limit requests are stored but answered `429 + Retry-After: 1` |
| `RETENTION_HOURS` | `168` | GC window for old requests |
| `MAX_BODY_BYTES` | `1048576` | Bodies above this are rejected with 413 (never truncated-and-verified) |
| `ACCESS_TOKEN` | unset | Gates UI + `/api/*`; `/hook/*` stays public by design |
| `PUBLIC_URL` | unset | Base URL rendered in capture URLs behind a tunnel |

## Layout

```
omnihook/
├── cmd/omnihook/          binary entry (up|version + DB-backed subcommands)
├── internal/
│   ├── api/               REST + SSE + UI server (+ hermetic tests)
│   ├── capture/           raw-body capture handler + SSE fan-out hub + rate gate
│   ├── cli/               new/list/show/replay/verify/gc subcommands
│   ├── config/            env config
│   ├── db/                SQLite open + migrate (WAL)
│   ├── forward/           async forward worker (records to replays, SSRF-guarded)
│   ├── gc/                retention cleanup (CLI one-shot + hourly scheduler)
│   ├── ratelimit/         per-IP token bucket for capture responses
│   ├── outbound/          shared target policy and header filtering
│   ├── slug/              shared endpoint-name validation
│   ├── replay/            replay client (options, re-sign, SSRF guard)
│   ├── verify/            stripe|github|standard|razorpay|shopify|generic + chain + re-sign
│   └── webui/             inbox + login UI, embedded in the binary (`WEB_DIR` overrides)
├── migrations/            embedded, versioned SQL upgrades (single schema source)
├── scripts/checkdocs/     docs-freshness gates (CHANGELOG, README env table, migrations and documented capabilities)
├── docs/                  PROVIDERS.md (connect guides + test results)
│                          MANUAL-TEST.md (hands-on playbook, per-OS)
│                          architecture.md / flow.md / why.md / storage.md
├── .github/workflows/     CI (make verify)
├── Dockerfile / compose / .goreleaser.yml / Makefile
└── README / AGENTS / CONTRIBUTING / CHANGELOG / LICENSE
```

## Development

```bash
go vet ./... && go test ./... && go build ./...
```

Branching: `main` = releases, `develop` = integration, `feat/*` for work. See [CONTRIBUTING.md](CONTRIBUTING.md).

