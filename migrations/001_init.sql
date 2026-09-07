-- OmniHook v1 schema (idempotent, SQLite, WAL mode set at runtime).
-- Endpoints are public capture URLs; requests store raw bytes for byte-identical replay.

CREATE TABLE IF NOT EXISTS endpoints (
  slug TEXT PRIMARY KEY,
  name TEXT NOT NULL DEFAULT '',
  provider TEXT NOT NULL DEFAULT 'generic',
  secret_ref TEXT NOT NULL DEFAULT '',
  target_url TEXT NOT NULL DEFAULT '',
  response_status INTEGER NOT NULL DEFAULT 200,
  response_body TEXT NOT NULL DEFAULT '{"ok":true}',
  response_content_type TEXT NOT NULL DEFAULT 'application/json',
  created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  expires_at DATETIME
);

CREATE TABLE IF NOT EXISTS requests (
  id TEXT PRIMARY KEY,
  endpoint_slug TEXT NOT NULL REFERENCES endpoints(slug) ON DELETE CASCADE,
  method TEXT NOT NULL,
  path TEXT NOT NULL DEFAULT '/',
  query TEXT NOT NULL DEFAULT '',
  headers TEXT NOT NULL DEFAULT '{}',
  content_type TEXT NOT NULL DEFAULT '',
  body BLOB,
  body_size INTEGER NOT NULL DEFAULT 0,
  truncated INTEGER NOT NULL DEFAULT 0,
  verify_status TEXT NOT NULL DEFAULT 'SKIPPED',
  verify_error TEXT NOT NULL DEFAULT '',
  fix_hint TEXT NOT NULL DEFAULT '',
  received_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_requests_slug_time ON requests(endpoint_slug, received_at DESC);

CREATE TABLE IF NOT EXISTS replays (
  id TEXT PRIMARY KEY,
  request_id TEXT NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
  target_url TEXT NOT NULL,
  status_code INTEGER NOT NULL DEFAULT 0,
  latency_ms INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_replays_req_time ON replays(request_id, created_at DESC);
