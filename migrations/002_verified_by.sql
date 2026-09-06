-- 002: record the effective verification provider per request (review #12, #14).
-- Older rows keep '' (unknown); re-sign falls back to the endpoint provider.
ALTER TABLE requests ADD COLUMN verified_by TEXT NOT NULL DEFAULT '';
