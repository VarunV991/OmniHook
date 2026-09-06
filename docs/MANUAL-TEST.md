# Manual test playbook (Windows PowerShell 5.1, v0.2.0)

End-to-end tour of the shipped binary. Each step states what to run and what
good looks like. Total: ~15 minutes. All localhost, no account, no tunnel
needed except step 8.

## 0. Build

```powershell
cd C:\Users\varun\Documents\OpenCode\OmniHook
go build -o bin\omnihook.exe .\cmd\omnihook
.\bin\omnihook.exe version   # dev on develop, v0.2.0 on main
```

## 1. Start the server

```powershell
$env:DATA_DIR = "$env:TEMP\omnihook-manual"
.\bin\omnihook.exe up --port 8080
```

Expect:

```
OmniHook dev listening on http://localhost:8080
UI: http://localhost:8080/
Health: http://localhost:8080/health
```

In a second shell: `Invoke-RestMethod http://localhost:8080/health` →
`{"ok":"true","db":"up","version":"dev"}`. Open the UI in a browser.

## 2. Create endpoints (CLI + UI)

```powershell
$env:DATA_DIR = "$env:TEMP\omnihook-manual"
.\bin\omnihook.exe new stripe1 --provider stripe --secret whsec_test123 --target http://localhost:3000/hook
.\bin\omnihook.exe new gh1 --provider github --secret mysecret
.\bin\omnihook.exe list
```

Expect `capture_url: http://localhost:8080/hook/stripe1`, and `list` shows both
slugs with providers. (The server need not run for CLI: it uses the DB file.)

## 3. Capture a plain webhook

```powershell
Invoke-WebRequest http://localhost:8080/hook/gh1/push -Method POST `
  -ContentType 'application/json' -Body '{"ref":"refs/heads/main"}' -UseBasicParsing
```

UI: new row appears live (`SKIPPED` badge — no signature headers, correct).
`.\bin\omnihook.exe show <id-from-list>` prints method/path/body.

## 4. Capture a Stripe-signed webhook (PASS path)

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

UI row shows **PASS**. Click it: headers, pretty body, no fix hint.

## 5. Tampered body (FAIL + fix hint)

Resend step 4 with `-Body '{"id":"evil"}'` but the **same** `$hdr`.
Expect **FAIL** + yellow fix box naming your framework
(Express `express.raw`, Spring `byte[]`, FastAPI `request.body()`, Django `request.body`).

Offline drill (no server needed):

```powershell
@{ 'Stripe-Signature' = $hdr } | ConvertTo-Json -Compress | Set-Content h.json -NoNewline
Set-Content b.bin -Value $body -NoNewline
.\bin\omnihook.exe verify --provider stripe --secret whsec_test123 --headers @h.json --body @b.bin
echo $LASTEXITCODE   # 0 = PASS
Set-Content b.bin -Value '{"id":"evil"}' -NoNewline
.\bin\omnihook.exe verify --provider stripe --secret whsec_test123 --headers @h.json --body @b.bin
echo $LASTEXITCODE   # 2 = FAIL, prints fix hint
```

## 6. Replay (incl. multi + re-sign)

Start a dummy target in a third shell: `npx http-echo-server` or any local app
on `:3000`. Or replay at the UI itself:

```powershell
.\bin\omnihook.exe replay <id> --target http://localhost:8080/health
# status=200 latency_ms=...

# Retry-logic workout: same event 5x with 200ms gaps
.\bin\omnihook.exe replay <id> --target http://localhost:8080/health --times 5 --delay-ms 200

# Old capture whose timestamp expired: re-sign with the endpoint secret
.\bin\omnihook.exe replay <old-id> --target http://localhost:8080/health --resign
```

In the UI detail pane: set **times**, tick **re-sign**, Replay — results render
as one object (times=1) or a `results` array.

## 7. Forward mode + rate limit + GC

- Endpoint `stripe1` already has `--target http://localhost:8080/health`, so every
  capture auto-forwards (check server log / `replays` via re-`show`). Break it:
  `Invoke-RestMethod -Method PATCH http://localhost:8080/api/endpoints/stripe1`
  with `{"target_url":"http://127.0.0.1:1/x"}` → captures still return 200
  (forward failure never breaks capture).
- Rate limit: restart with `$env:RATE_LIMIT_RPS = '2'`, POST 4x fast → expect
  `200,200,429,429`, and `list` still shows 4 stored requests (evidence kept).
- GC: `.\bin\omnihook.exe gc --retention-hours 168` → `deleted 0 requests...`.
  (Aged rows are covered by `internal/gc` tests.)

## 8. Real provider (optional, ~5 min)

```powershell
cloudflared tunnel --url http://localhost:8080
$env:PUBLIC_URL = 'https://<you>.trycloudflare.com'
.\bin\omnihook.exe up
```

Stripe Dashboard → Webhooks → Add endpoint → `https://<you>.trycloudflare.com/hook/stripe1`
→ send test event → PASS row appears. GitHub: repo Settings → Webhooks →
same URL → Recent Deliveries → Redeliver replays without pushing.

## 9. Token-gated mode (optional)

Restart with `$env:ACCESS_TOKEN = 's3cret'`. `/hook/*` still public; UI/API
prompt once for the token and remember it. `curl` without
`Authorization: Bearer s3cret` → `401 unauthorized`.

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `HEALTH_NEVER_UP` in scripts | Server started after env set? `PORT`/`DATA_DIR` must be set **before** launch |
| `t=...,v1=...` always FAIL | Body re-serialized (pretty-printed) — send raw bytes; see fix hint |
| `timestamp outside tolerance` | Clock skew, or replaying an old capture — use `--resign` |
| UI empty behind tunnel | `PUBLIC_URL` not set, or provider pointed at stale ngrok URL |
| `slug ... 400` | Slugs: `^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$` |
