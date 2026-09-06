# Provider guides — connect, verify, troubleshoot

How to point each provider at OmniHook, what PASS looks like, and what to do
when it says FAIL. All signature schemes verified by `TestProviderMatrix`
(21 cases) plus a live end-to-end run against the shipped binary on 2026-09-06
(see Results §6).

Prerequisites for real (non-simulated) events: expose OmniHook publicly once —
`cloudflared tunnel --url http://localhost:8080`, then
`PUBLIC_URL=https://<you>.trycloudflare.com omnihook up`.

## 1. Stripe — `checkout.session.completed` and friends

Connect:
1. `omnihook new stripe1 --provider stripe --secret whsec_YOURS --target http://localhost:3000/webhooks/stripe`
2. Stripe Dashboard → Developers → Webhooks → Add endpoint → `https://<tunnel>/hook/stripe1` → select events → copy **Signing secret** (`whsec_...`) into the endpoint secret.
3. Trigger a test payment. Inbox row should read `PASS`.

Scheme: `Stripe-Signature: t=<unix>,v1=<hex>`, `v1 = HMAC_SHA256(secret, "<t>.<raw_body>")`, 5-min tolerance.
Sample headers/body shape:
```json
{ "Stripe-Signature": "t=1725600000,v1=ab12..." }
```
```json
{"id":"evt_1","object":"event","type":"checkout.session.completed","data":{"object":{"id":"cs_test_123","amount_total":5000}}}
```
Troubleshooting:
- `signature mismatch` → you parsed JSON before verifying. Express: `express.raw({type:'application/json'})`; Spring Boot: `@RequestBody byte[] raw`; FastAPI: `await request.body()`; Django: `request.body`.
- `timestamp outside 5-min tolerance` → clock skew (NTP) or replaying an old capture (expected — re-capture fresh).
- CLI drill: save headers/body from `omnihook show <id> --json`, then `omnihook verify --provider stripe --secret whsec_... --headers @h.json --body @b.bin` (exit 0 PASS, 2 FAIL).

## 2. GitHub — `push`, `pull_request`, `workflow_run`

Connect:
1. `omnihook new gh1 --provider github --secret YOUR_SECRET`
2. Repo → Settings → Webhooks → Add webhook → Payload URL `https://<tunnel>/hook/gh1`, Content type `application/json`, Secret = same value → Add. Use **Recent Deliveries → Redeliver** to replay without pushing.

Scheme: `X-Hub-Signature-256: sha256=<hex>`, `HMAC_SHA256(secret, raw_body)`. Legacy `X-Hub-Signature` (SHA1) alone → FAIL with upgrade hint.
Sample body shape: `{"ref":"refs/heads/main","repository":{"full_name":"acme/app"},"commits":[...]}`.
Troubleshooting: mismatch almost always means secret paste error (trailing spaces/newlines) or a proxy re-serializing JSON.

## 3. Standard Webhooks — Svix / OpenAI / Anthropic / Clerk / Resend / Supabase

Connect: same as above with `--provider standard`; secret is the `whsec_...` value from the sender dashboard.
Scheme: `Webhook-Id`, `Webhook-Timestamp` (epoch seconds), `Webhook-Signature: v1,<base64> [v1,<base64> ...]`; signed content `<id>.<timestamp>.<raw>`; key = base64-decoded secret after `whsec_`; 5-min tolerance. Spec: https://standardwebhooks.com.
Note: Clerk/Resend/Svix-compatible senders share this shape — if headers match, the `standard` verifier handles them with no extra code.

## 4. Razorpay — `payment.captured` and friends

Connect:
1. `omnihook new rzr1 --provider razorpay --secret YOUR_SECRET`
2. Dashboard → Settings → Webhooks → Add → `https://<tunnel>/hook/rzr1`, secret = same value.

Scheme: `X-Razorpay-Signature: <hex>`, `HMAC_SHA256(secret, raw_body)`.

## 5. Anything else — Generic HMAC

No built-in verifier? The endpoint still captures everything (`SKIPPED`, never hard-failed). For HMAC современным: reuse the `generic` verifier pattern (`header + optional prefix + SHA256`) or open an issue with the provider's signing docs — new verifiers are ~30 lines + golden tests (see `internal/verify/razorpay.go`).

## 6. Test results (2026-09-06, commit `feat/cli` pre-merge)

Unit matrix `go test ./internal/verify/ -run TestProviderMatrix` — 21/21 PASS:

| Provider | valid | auto-detect | tampered→FAIL+hint | wrong secret→FAIL | expired→FAIL | no secret→FAIL+hint |
|---|---|---|---|---|---|---|
| Stripe | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| GitHub | ✅ | ✅ | ✅ | ✅ | n/a | ✅ |
| Standard | ✅ | ✅ | ✅ | ✅ | ✅ | n/a |
| Razorpay | ✅ | ✅ | ✅ | ✅ | n/a | n/a |
| Unknown | — | — | — | — | — | SKIPPED ✅ |

Live binary E2E (`bin/omnihook.exe`, server + CLI, real HMAC over HTTP):
`new` → signed `POST /hook/stripe1` → `list` shows 1 request → inbox `VERIFY_STATUS: PASS` → `show` prints body → `verify` exit 0 → tampered body exit 2 with Express/Spring/FastAPI/Django fix hint → `replay` 200 in 20ms → `gc` 0 deleted → **E2E_PASS**.

Reproduce anytime: `go test ./...` (unit) and the CLI loop above against `omnihook up --port <free>`.
