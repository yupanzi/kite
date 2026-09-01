@AGENTS.md

## Fork layout (yupanzi/kite)

This is a fork of `kite-org/kite`. `AGENTS.md` above describes the upstream
project; everything here is fork-only and has no upstream counterpart.

`main` is an exact upstream mirror, force-pushed daily by `sync-upstream.yml`.
`master` is the trunk and the default branch. Never commit to `main` — the next
sync overwrites it.

The fork only *adds* workflows and never edits upstream ones, which is why
`.github/` has never produced a sync conflict. Keep it that way:

- `ci.yml`, `e2e.yml`, `release.yaml` are upstream's and trigger on `main` only,
  so they never see `master`.
- `fork-ci.yml` is the CI for `master`: lint, type-check, and tests on both
  sides — the checks `internal-release.yml` does not run.
- `internal-release.yml` publishes every `master` push to GHCR.
- `sync-upstream.yml` mirrors `main` and merges it into `master`.

The sync merge reaches `master` only after the merged tree is proven to build.
`git merge` reconciles text, not semantics, and this fork's `pkg/ai` and
`ui/src/components/ai-chat` changes sit on files upstream refactors often, so a
clean merge can still fail to compile — that is how a broken Go call site and a
broken TS type each reached `master` before the gate existed. On a text conflict
or a failed build, `master` is left untouched and a `main` -> `master` PR is
opened; resolve it with `scripts/sync-upstream.sh`, verify with `make
pre-commit` *and* `go test ./...`, then push. `pre-commit` is `format lint
type-check` and runs no Go tests, so it stays green on a resolution that
reverts fork behaviour a fork-only test asserts — and a fork-only test never
conflicts, so it survives a wholesale "take upstream" and only fails
afterwards. That is the 2026-09-01 OCI sync: the fork's own feature returned
as upstream PR #666, and upstream's `pkg/helm/content.go` still carried the
merge base's version sort that `pkg/helm/content_test.go` asserts against.

`static/` is gitignored and `static.go` carries `//go:embed static`, so nothing
on the Go side compiles — not `build`, not `vet`, not `test` — until the
frontend has been built at least once. Any workflow or script that checks Go
code must build the frontend first.
