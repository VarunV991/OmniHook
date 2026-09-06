## PR checklist (required for merge to develop/main)

- [ ] `make verify` passes (`gofmt` clean, `go vet`, `go test`, `go build`, `checkdocs`)
- [ ] Behavior change covered by tests (`internal/*/*_test.go` or hermetic API test)
- [ ] `CHANGELOG.md` `[Unreleased]` has an entry (enforced by `checkdocs` in CI)
- [ ] `README.md` updated if flags, endpoints, env vars, or quickstart changed (env table enforced by `checkdocs`)
- [ ] `AGENTS.md` updated if branching, sandbox, conventions, or repo layout changed
- [ ] `PLAN.md` §2 scope table updated if scope moved (P0 ↔ Deferred)
