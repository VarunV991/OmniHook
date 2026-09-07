# Why OmniHook exists

OmniHook shortens the loop between receiving a webhook and fixing the code that
handles it. Its useful unit is a saved real request: receive once, inspect it,
then replay it as often as needed.

## The problem in concrete terms

A payment service calls your webhook after a payment. Your handler fails. You
change the code, but now need the same input again. Triggering another payment
can be slow and may produce a different event. Logs may omit the original body
or alter its formatting, making signature failures hard to reproduce.

A tunnel solves reachability: it gives an outside sender a route to your local
server. It does not by itself provide a persistent debugging inbox. OmniHook
adds saved bodies, signature results, and recorded replay attempts behind that
route.

## Why these design choices

| Choice | Benefit | Cost or limit |
| --- | --- | --- |
| Local database | Captures stay in storage you control | You handle access, backups, and disk space |
| One Go binary with embedded UI | No frontend build or database service to run | One process and local SQLite bound the scale |
| Preserve raw body bytes | Reproduce input and diagnose signature mismatches | Captures may contain sensitive credentials or personal data |
| Save before forwarding | A failing app does not destroy the debugging evidence | Provider success does not mean downstream success |
| Bounded forwarding queue | A slow app cannot create unlimited workers | Bursts can be dropped; inspect delivery records |
| Separate verify and replay | Explain a failed signature, then retry deliberately | Re-signing requires secrets and changes the original signature |

## When to use it

Use OmniHook while developing an integration, comparing good and bad requests,
testing a local handler against captured payloads, or debugging an old event
without depending on the sender being available.

Supported built-in checks cover Stripe, GitHub, Standard Webhooks (including
compatible Svix/Clerk headers), Razorpay, and Shopify. Other HTTP payloads can
still be captured and replayed. Custom Generic HMAC header configuration is not
exposed by the current API or CLI; unknown traffic can be `SKIPPED`.

## Where the boundary is

OmniHook is not a production webhook gateway. It does not provide durable retry
queues, delivery guarantees, tenant isolation, or a hosted relay. If customers
depend on every downstream delivery completing, this debugging architecture is
not enough.

It is also not a substitute for signature verification in your application.
Seeing `PASS` in OmniHook tells you about its check on its stored input. Your
own handler still needs the correct secret, raw body handling, and replay
protections.

Success is measurable: you can take one failing captured event, reproduce the
failure locally, change your handler, and show a successful replay. Stars,
feature count, and a passing signature badge alone do not establish that.
