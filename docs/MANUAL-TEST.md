# Manual test playbook (v0.2.0)

End-to-end tour of the shipped binary. Each step states what to run and what
good looks like. Total: ~15 minutes. All localhost, no account, no tunnel
needed except step 8.

Conventions: `<repo>` = your OmniHook checkout. Windows commands are
PowerShell 5.1; Linux/macOS commands are bash. The server runs in the
foreground in its own terminal on every OS.

## 0. Build

Windows:

```powershell
cd <repo>
go build -o bin\omnihook.exe .\cmd\omnihook
.\bin\omnihook.exe version   # dev on develop, v0.2.0 on main
```

Linux / macOS:

```bash
cd <repo>
go build -o bin/omnihook ./cmd/omnihook
./bin/omnihook version   # dev on develop, v0.2.0 on main
```

## 1. Start the server

Windows:

```powershell
$env:DATA_DIR = "$env:TEMP\omnihook-manual"
.\bin\omnihook.exe up --port 8080
```

Linux / macOS:

```bash
export DATA_DIR="$TMPDIR/omnihook-manual"   # or /tmp/omnihook-manual
./bin/omnihook up --port 8080
```

Expect:

```
OmniHook dev listening on http://localhost:8080
UI: http://localhost:8080/
Health: http://localhost:8080/health
```

Health check — Windows: `Invoke-RestMethod http://localhost:8080/health`;
Linux/macOS: `curl -s http://localhost:8080/health`. Expect
`{"ok":"true","db":"up",...}`. Open the UI in a browser.

## 2. Create endpoints (CLI + UI)

Set the same `DATA_DIR` in the second shell (the CLI reads the DB file
directly — the server need not run for CLI commands).

Windows:

```powershell
$env:DATA_DIR = "$env:TEMP\omnihook-manual"
.\bin\omnihook.exe new stripe1 --provider stripe --secret whsec_test123 --target http://localhost:3000/hook
.\bin\omnihook.exe new gh1 --provider github --secret mysecret
.\bin\omnihook.exe list
```

Linux / macOS:

```bash
export DATA_DIR="/tmp/omnihook-manual"
./bin/omnihook new stripe1 --provider stripe --secret whsec_test123 --target http://localhost:3000/hook
./bin/omnihook new gh1 --provider github --secret mysecret
./bin/omnihook list
```

Expect `capture_url: http://localhost:8080/hook/stripe1`, and `list` shows both
slugs with providers.

## 3. Capture a plain webhook

Windows:

```powershell
Invoke-WebRequest http://localhost:8080/hook/gh1/push -Method POST `
  -ContentType 'application/json' -Body '{"ref":"refs/heads/main"}' -UseBasicParsing
```

Linux / macOS:

```bash
curl -s -X POST http://localhost:8080/hook/gh1/push \
  -H 'Content-Type: application/json' -d '{"ref":"refs/heads/main"}'
```

UI: new row appears live (`SKIPPED` badge — no signature headers, correct).
Show it — Windows: `.\bin\omnihook.exe show <id>`; Linux/macOS: `./bin/omnihook show <id>`.

## 4. Capture a Stripe-signed webhook (PASS path)

Compute `t=<unix>,v1=<hmac>` over `<t>.<raw_body>` with secret `whsec_test123`.
Portable (any OS with python3):

```bash
python3 - <<'EOF'
import hmac, hashlib, time, json
secret = b'whsec_test123'
body = b'{"id":"evt_1","type":"checkout.session.completed"}'
ts = int(time.time())
sig = hmac.new(secret, f"{ts}.".encode() + body, hashlib.sha256).hexdigest()
print(json.dumps({"Stripe-Signature": f"t={ts},v1={sig}"}))
print(body.decode())
EOF
```

Save the two output lines as headers JSON and body file, then send with the
header (curl shown; PowerShell: `Invoke-WebRequest ... -Headers @{'Stripe-Signature'=$hdr}`):

```bash
HDR=$(python3 -c "..." )  # or paste the t=...,v1=... value
curl -s -X POST http://localhost:8080/hook/stripe1 \
  -H 'Content-Type: application/json' -H "Stripe-Signature: $HDR" \
  -d '{"id":"evt_1","type":"checkout.session.completed"}'
```

UI row shows **PASS**. Click it: headers, pretty body, no fix hint.

Windows without python3 (`py -3` also works) — native PowerShell:

```powershell
$secret = 'whsec_test123'
$body = '{"id":"evt_1","type":"checkout.session.completed"}'
$ts = [DateTimeOffset]::UtcNow.ToUnixTimeSeconds()
$msg = [string]$ts + '.' + $body
$hm = New-Object System.Security.Cryptography.HMACSHA256
$hm.Key = [Text.Encoding]::UTF8.GetBytes($secret)
$sig = [BitConverter]::ToString($hm.ComputeHash([Text.Encoding]::UTF8.GetBytes($msg))).Replace('-','').ToLower()
$hdr = 't=' + $ts + ',v1=' + $sig
Invoke-WebRequest http://localhost:8080/hook/stripe1 -Method POST `
  -ContentType 'application/json' -Headers @{'Stripe-Signature' = $hdr} `
  -Body $body -UseBasicParsing
```

## 5. Tampered body (FAIL + fix hint)

Resend step 4 with body `{"id":"evil"}` but the **same** signature header.
Expect **FAIL** + yellow fix box naming your framework
(Express `express.raw`, Spring `byte[]`, FastAPI `request.body()`, Django `request.body`).

Offline drill (no server needed) — save headers/body from
`omnihook show <id> --json` into `h.json` / `b.bin`, then:

```bash
./bin/omnihook verify --provider stripe --secret whsec_test123 --headers @h.json --body @b.bin
echo $?   # 0 = PASS (bash) — PowerShell: echo $LASTEXITCODE
# tamper b.bin, re-run → exit 2 = FAIL, prints fix hint
```

## 6. Replay (incl. multi + re-sign)

Start a dummy target (any local app on `:3000`) or replay at the UI itself:

```bash
./bin/omnihook replay <id> --target http://localhost:8080/health
# status=200 latency_ms=...

# Retry-logic workout: same event 5x with 200ms gaps
./bin/omnihook replay <id> --target http://localhost:8080/health --times 5 --delay-ms 200

# Old capture whose timestamp expired: re-sign with the endpoint secret
./bin/omnihook replay <old-id> --target http://localhost:8080/health --resign
```

(Windows: same commands with `.\bin\omnihook.exe`.)
In the UI detail pane: set **times**, tick **re-sign**, Replay — results render
as one object (times=1) or a `results` array.

## 7. Forward mode + rate limit + GC

- Endpoint `stripe1` already has a target, so every capture auto-forwards
  (check via re-`show`). Break it: `PATCH /api/endpoints/stripe1` with
  `{"target_url":"http://127.0.0.1:1/x"}` → captures still return 200
  (forward failure never breaks capture).
- Rate limit: restart with `RATE_LIMIT_RPS=2` (`$env:RATE_LIMIT_RPS='2'` on
  Windows), POST 4x fast → expect `200,200,429,429`, and `list` still shows
  4 stored requests (evidence kept).
- GC: `omnihook gc --retention-hours 168` → `deleted 0 requests...`.
  (Aged rows are covered by `internal/gc` tests.)

## 8. Real provider (optional, ~5 min)

```bash
cloudflared tunnel --url http://localhost:8080
# then restart with PUBLIC_URL=https://<you>.trycloudflare.com
```

Stripe Dashboard → Webhooks → Add endpoint → `https://<you>.trycloudflare.com/hook/stripe1`
→ send test event → PASS row appears. GitHub: repo Settings → Webhooks →
same URL → Recent Deliveries → Redeliver replays without pushing.

## 9. Token-gated mode (optional)

Restart with `ACCESS_TOKEN=s3cret` (`$env:ACCESS_TOKEN='s3cret'` on Windows).
`/hook/*` still public; UI/API prompt once for the token and remember it.
Unauthenticated API calls → `401 unauthorized`.

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `HEALTH_NEVER_UP` in scripts | Server started after env set? `PORT`/`DATA_DIR` must be set **before** launch |
| `t=...,v1=...` always FAIL | Body re-serialized (pretty-printed) — send raw bytes; see fix hint |
| `timestamp outside tolerance` | Clock skew, or replaying an old capture — use `--resign` |
| UI empty behind tunnel | `PUBLIC_URL` not set, or provider pointed at a stale tunnel URL |
| `slug ... 400` | Slugs: `^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$` |
| Windows: binary won't run from a pipe | Build with `.exe` suffix: `go build -o bin\omnihook.exe ...` |
