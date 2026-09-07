# Storage, upgrades, and backups

The database contains captured payloads, request headers, and endpoint signing
secrets in plaintext. An API response masking a secret does not encrypt the
stored value. Keep the data directory and backups private; never commit them.

## Upgrades

The binary embeds numbered SQL migrations from `migrations/`. On open it applies
pending upgrades transactionally and records their versions. Existing databases
from before version tracking are adopted without deleting saved requests.
Back up before upgrading. Database upgrades are forward changes: do not assume
an older binary can safely open a database upgraded by a newer one.

SQLite uses WAL mode, a five-second busy timeout, and foreign-key checks.
Concurrent readers are supported, but writes still serialize. CLI and server
access can contend; an exhausted busy timeout is an error, not proof a request
was saved. Inspect capture errors and `/health` when diagnosing disk or lock
problems.

## Permissions

On Unix, new database files request owner-only access and new data directories
request owner-only directory access. Existing shared directories and platform
ACLs need separate review. On Windows, Unix permission bits do not enforce an
owner-only ACL: place the database in a user-private directory and restrict its
Windows permissions. Keep WAL/SHM companion files and backups under the same
access policy. This is filesystem protection, not encryption.

## A safe backup and restore

1. Stop OmniHook gracefully and stop other commands using the same database.
2. Copy the database and any remaining `-wal` / `-shm` companion files together
   into a private backup directory. Do not copy only the main file while a
   writer is active; committed changes may still be in the WAL.
3. To restore, stop every writer, preserve the current database set separately,
   and copy the backup set into a fresh private data directory. Start with
   `DATA_DIR` pointing there (or `DATABASE_URL` pointing at the restored file).
4. Check `/health`, endpoint settings, and a known saved request before using
   the restored instance. Restoring a backup also restores its stored secrets.

For online backups, use a SQLite-aware backup tool; an ordinary live file copy
is not the equivalent of a consistent database backup.

## Retention is not erasure

`RETENTION_HOURS` controls how old saved requests may be. The scheduler first
runs after an hour, then hourly; run `omnihook gc` for immediate cleanup.
Expired empty endpoints are also removed. Request deletion cascades to related
replay records.

Deleting rows does not guarantee the database file shrinks, does not erase old
backups, and does not guarantee forensic removal of old bytes. Reclaiming disk
space with SQLite maintenance is a separate operation that requires a backup,
free disk space, and planned downtime. No secure-erasure guarantee is made.
