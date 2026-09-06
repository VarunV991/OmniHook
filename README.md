# OmniHook — local-first universal webhook inbox (MIT)

Capture, verify, replay webhooks locally. No account. Data stays in SQLite.

See **PLAN.md** for full requirements, scope, build/test/launch plan.

## Quickstart (60s)

```bash
go run ./cmd/omnihook up
# UI: http://localhost:8080/  Health: http://localhost:8080/health

# create endpoint
curl -s -X POST localhost:8080/api/endpoints -H 'Content-Type: application/json' \
  -d '{"slug":"proj1","provider":"stripe"}'

# send a webhook
curl -X POST localhost:8080/hook/proj1 -H 'Content-Type: application/json' \
  -d '{"event":"ping"}'

# list + replay via UI or API
curl -s localhost:8080/api/endpoints/proj1/requests | head -c 500
```

Docker:

```bash
docker compose up --build
```

## What v1 does / doesn't do

Does: capture any method/path/body (raw bytes), verify Stripe/GitHub/Standard/Razorpay/Generic HMAC with fix hints, live SSE inbox, one-click replay to localhost with edit, single binary + SQLite.

Doesn't: prod retries/DLQ, transformations, customer portal, hosted relay. Those are non-goals (see PLAN.md §1.4).

## Raw-body fix hints (why verify fails)

- Express: `app.post('/hook', express.raw({type:'application/json'}))`
- Spring Boot: `@RequestBody byte[] raw` + `Mac.getInstance("HmacSHA256")`
- FastAPI: `raw = await request.body()`
- Django: `request.body` (never `request.POST` for HMAC)

## Tunnel (bring your own for v1)

```bash
cloudflared tunnel --url http://localhost:8080
# set PUBLIC_URL=https://<you>.trycloudflare.com so capture URLs render correctly
```

## Layout

```
cmd/omnihook      binary entry (up|version)
internal/config   env config
internal/db       SQLite open + migrate (WAL)
internal/verify   stripe|github|standard|razorpay|generic + chain (90%+ tests)
internal/capture  raw-body capture handler + SSE hub
internal/replay   replay client with SSRF guard
internal/api      REST + SSE + embedded UI server
web/              single-page inbox UI (embedded)
migrations/       idempotent SQL
```
