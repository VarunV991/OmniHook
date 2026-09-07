# Contributing to OmniHook

## Branching (develop → main)

- `main` = last stable release. Never commit directly. Release only: merge `develop`, tag `vX.Y.Z`.
- `develop` = integration. Feature work: `git checkout develop && git checkout -b feat/<short-name>`, merge back into `develop` (squash ok, Conventional Commits).
- Hotfix: branch `hotfix/*` from `main`, merge into **both** `main` and `develop`.

## Local dev

```bash
go vet ./... && go test ./...          # must pass before any push
go run ./cmd/omnihook up               # UI :8080, health :8080/health
PORT=18081 DATA_DIR=$RUNNER_TEMP/smoke go run ./cmd/omnihook up   # parallel instance
```

## Conventions

- Go 1.22+, `gofmt` clean, no ORM (`database/sql` + numbered SQL in `migrations/`).
- Verifiers operate on **raw bytes**; always add golden positive + negative tests in `internal/verify`.
- Capture response must never fail because forwarding failed (forward is async).
- Secrets: never log full values; UI shows suffix only.
- Docs: update `CHANGELOG.md` Unreleased and relevant README/guides with every feature PR; track scope in issues.

## Release checklist (maintainer)

1. `develop` green: `go vet`, `go test`, binary smoke (health→create→capture→replay).
2. Update `CHANGELOG.md` (move Unreleased → `vX.Y.Z` + date), bump version string if needed.
3. `gh pr create --base main --head develop`, merge, `git tag vX.Y.Z main`, `git push origin main --tags`.
4. `gh release create vX.Y.Z --generate-notes` (GoReleaser assets attach via CI if configured).

