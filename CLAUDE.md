@AGENTS.md

## Fork layout (yupanzi/kite)

This is a fork of `kite-org/kite`. `AGENTS.md` above describes the upstream
project; everything here is fork-only and has no upstream counterpart.

`AGENTS.md` is upstream's file and is kept byte-identical to it. Fork-specific
guidance belongs here instead — the 2026-09-06 sync conflict was upstream
rewriting `AGENTS.md` on top of fork-only paragraphs that had been added to it.
Where upstream's text no longer describes this fork, the correction lives below;
do not fix it in `AGENTS.md`.

- `AGENTS.md` says to type-check with `pnpm --dir ui exec tsc --noEmit -p
  tsconfig.app.json` because the root config has no source files. That is
  upstream's state, not this fork's: `648020b` pointed `ui`'s `type-check`
  script at `tsc -b --noEmit`, so `make type-check` really checks. Use it, and
  keep it in `pre-commit`. Build mode is what makes the references-only
  `ui/tsconfig.json` resolve its project references.

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
pre-commit` *and* `go test ./...`, then push. While that PR is open the daily
sync keeps failing on purpose — it used to exit 0 once the PR existed, which
made every later run green while `master` fell four days behind upstream. `pre-commit` is `format lint
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

## AI request budgets

The fork's `pkg/ai` Anthropic path has no upstream counterpart. Two settings
control model behaviour and they are not interchangeable:

- `AIMaxTokens` is a per-response ceiling. On current Claude models thinking and
  answer text share it, so a small value truncates the answer. It is sent to the
  provider as configured — never clamped, floored, or rejected, because only the
  provider knows the configured model's real limit.
- `AIEffort` (`output_config.effort`) is the reasoning-depth knob and the only
  one: `budget_tokens` is removed on current models and returns 400. Levels are
  `low`/`medium`/`high`/`xhigh`/`max`, default `xhigh`. Anthropic path only.

`anthropicModelSupportsModernFeatures` gates effort, adaptive thinking, and
context management behind a deny list of model-name substrings. That list tracks
*request-surface support*, not lifecycle — Opus 4.5, Sonnet 4.5, and Haiku 4.5
are all still sold but reject the modern surface, and retired first-party models
stay listed because they remain available through Bedrock and Google Cloud. A
false negative here silently downgrades a capable model; there is no retry on a
400, so widening the gate needs a fallback path first.

SSE streams (`newStreamSender` in `pkg/ai/handler.go`) emit a keepalive comment
every 20s. An agent turn is legitimately silent while a tool runs, and
ingress-nginx closes a connection after 60s of backend silence. Chart timeouts
and the ingress annotation examples in `charts/kite/values.yaml` are the other
half of this — change them together.
