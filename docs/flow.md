# A webhook debugging session

Suppose a payment service sends an event when a customer pays, but your local
application returns an error. OmniHook lets you save that event once, fix your
code, and replay the saved input without making another payment.

## 1. Create an inbox

Run `omnihook up`, open the browser inbox, and create an endpoint such as
`payments`. Choose the provider and enter its webhook signing secret if you
want signature checks. The capture path is `/hook/payments`.

A provider on the internet cannot reach your localhost directly. Use your own
tunnel and configure its public capture URL in the provider. Set `ACCESS_TOKEN`
before exposing the server. For a first experiment, a local `curl` request
works without a tunnel; [MANUAL-TEST.md](MANUAL-TEST.md) has per-shell examples.

## 2. Receive a request

The capture handler checks the inbox, reads the body up to the configured limit,
checks the signature, and saves the original bytes with the result. It then
notifies connected browser tabs and optionally queues forwarding.

The response to the provider is the endpoint's configured mock response. It is
not a report of what your application did. Storage failures return an error;
oversized bodies return 413; the capture rate limiter can save a request but
return 429 to ask the sender to slow down.

## 3. Inspect the evidence

Choose the request in the inbox. Inspect headers, body, verification error, and
any available correction hint. JSON formatting is for reading. For exact binary
bytes, use `GET /api/requests/<id>/body` or `omnihook show <id> --raw out.bin`.

A signature failure may mean a wrong secret, missing header, expired timestamp,
or changed body. Correct the specific cause. Framework raw-body examples help
with body changes; they do not fix a wrong secret or clock.

The API loads saved requests with stable cursor pagination. The current inbox
view requests the newest 100 records; older pages can be retrieved directly with
`GET /api/requests?limit=...&cursor=...` or the endpoint-scoped equivalent. Live
notifications trigger refreshes; an SSE notification is only a prompt to read
persisted data, so refreshing after a connection loss is safe.

## 4. Replay to your application

Set the target to the full handler URL, for example
`http://localhost:3000/webhooks/payments`, and replay. OmniHook records the
HTTP status, latency, and any delivery error. A target HTTP 500 means delivery
reached the application and the application failed; a connection error means
no HTTP response was received.

The target URL replaces the whole original URL: OmniHook does not append the
captured path or query. Original body bytes are used unless you explicitly edit
them. Connection-specific headers are removed. Redirects are returned as
results rather than followed, so a 302 cannot silently turn a POST into a GET.

For an old signed event, use `--resign` or the UI re-sign option when the provider
supports it and the endpoint has the required secret. This creates a new
signature; it is no longer a reproduction of the original signature. Explicit
header edits take precedence over generated headers.

## 5. Repeat, automate, and clean up

Fix your application and replay again. You can request multiple attempts. Use
`--fail-on-http-error` in CLI scripts when an HTTP 400 or higher should fail the
command; delivery/transport errors already fail it by default.

If you set an endpoint's forwarding target, new captures are sent there
automatically through a bounded queue. Marked OmniHook deliveries are not
forwarded again, which prevents simple delivery loops. No durable retry system
is provided.

Retention cleanup runs hourly; `omnihook gc` runs it on demand. Deleting rows
removes them from the inbox but is not secure disk erasure. Backups also retain
whatever they contained when created; see [storage.md](storage.md).
