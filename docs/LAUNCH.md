# Launch plan (Reddit + dev forums — no spam, value-first)

Status: not started. Execute after v0.2.0 (shipped 2026-09-06).
Promoted from the original build plan so the repo stays public-focused.

Principles: no cross-post blast day 1. One home post + help in comments for
2 weeks. Demo GIF > text. Disclose OSS + MIT. No affiliate links.

## 1. Assets to prepare BEFORE posting

- 30s GIF: `stripe trigger -> inbox appears -> FAIL->hint->fix -> Replay 200`.
  Plus 90s YouTube unlisted.
- README quickstart copy-paste (5 lines), `docker run` one-liner, `curl` examples.
- 3 code snippets ready to paste in comments: Express raw-body, Spring Boot
  `byte[]`, FastAPI `request.body()` (all in `docs/PROVIDERS.md`).

## 2. Where + angle (sequence over 14 days)

1. **r/stripe (Day 1, home):** `I got tired of re-triggering payments to debug webhooks, so I built a local inbox with one-click replay (OSS, Go)`. Pain (50 replays for 1 bug), GIF, what it does/doesn't do, ask: `What provider should I add next?` Respond to every comment with a snippet.
2. **r/selfhosted (Day 3):** privacy + single binary + SQLite, no account. `Self-hosted webhook.site alternative — single Go binary, SQLite, no expiry (MIT)`. Include Compose + `PUBLIC_URL` + retention config. Ask for ARM testing.
3. **r/golang (Day 5):** technical. `Show me your SSE + SQLite patterns — I built a webhook debugger, looking for review on raw-body handling`. Link `internal/verify`, ask for critique. This gets contributors.
4. **r/webdev + r/Backend (Day 7-9):** tutorial, not promo. `How I debug Stripe webhooks locally without re-triggering payments (capture -> breakpoint -> replay)`. Link repo at end as "I packaged this".
5. **Dev.to + HN Show HN (Day 10-12):** Dev.to canonical tutorial with Spring + FastAPI snippets. HN: `Show HN: OmniHook – local-first webhook inbox with signature debugging` — post morning ET, stay 6h to answer.
6. **GitHub Topics + Discord:** topics already set (`webhooks`, `stripe`, `self-hosted`, ...). Share in Svix/Standard-Webhooks discussions, Hookdeck community (respectfully as dev-tool, not prod replacement), local JS/Java/Python Discords.

## 3. Comment-help playbook (ongoing)

- Search weekly: `webhook testing localhost`, `Stripe webhook signature fails`, `ngrok URL changes`. Answer with fix hint + `omnihook verify ...` example, not just link.
- Collect provider requests → roadmap issue, upvote-driven.
- Metrics: stars, time-to-first-replay feedback, verifier FAIL accuracy, tunnel guide hits. Kill what doesn't get comments.

## 4. Anti-spam checklist

- Read each sub's promo rules, get mod OK for r/stripe if needed, no duplicate posts, no AI-slop comments, disclose author, no paid plan to push (there is none).
