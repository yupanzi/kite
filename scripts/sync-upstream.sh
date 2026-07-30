#!/bin/bash

# Sync the upstream (kite-org/kite) `main` into this GitHub fork.
#
#   1. Fetch upstream `main`.
#   2. Force-push it to origin `main` (kept as an exact upstream mirror).
#   3. If the working tree is clean, merge it into `master` (the fork's
#      primary branch) and push.
#
# This is the manual counterpart of the scheduled GitHub Actions job
# (.github/workflows/sync-upstream.yml); use it for an on-demand sync, and to
# resolve the merge conflicts that make the scheduled job fail.
#
# Overridable via env:
#   UPSTREAM_URL    (default: https://github.com/kite-org/kite.git)
#   ORIGIN_REMOTE   (default: origin)   the GitHub fork remote
#   UPSTREAM_BRANCH (default: main)     branch to mirror
#   PRIMARY_BRANCH  (default: master)   fork primary branch to merge into

set -euo pipefail

UPSTREAM_URL="${UPSTREAM_URL:-https://github.com/kite-org/kite.git}"
ORIGIN_REMOTE="${ORIGIN_REMOTE:-origin}"
BRANCH="${UPSTREAM_BRANCH:-main}"
PRIMARY="${PRIMARY_BRANCH:-master}"

echo ">> Fetching ${BRANCH} from ${UPSTREAM_URL} ..."
git fetch --no-tags "$UPSTREAM_URL" "+${BRANCH}:refs/remotes/upstream/${BRANCH}"

echo ">> Mirroring upstream ${BRANCH} -> ${ORIGIN_REMOTE}/${BRANCH} ..."
git push --force "$ORIGIN_REMOTE" \
  "refs/remotes/upstream/${BRANCH}:refs/heads/${BRANCH}"

if ! git diff --quiet || ! git diff --cached --quiet; then
  echo ">> Working tree is not clean; mirror done, skipping the merge into ${PRIMARY}."
  echo "   When ready: git checkout ${PRIMARY} && git merge refs/remotes/upstream/${BRANCH} && git push ${ORIGIN_REMOTE} ${PRIMARY}"
  exit 0
fi

# The scheduled sync pushes merge commits to the remote primary branch daily, so
# a local checkout is routinely behind; fast-forward it first or the final push
# is rejected as non-fast-forward.
echo ">> Updating ${PRIMARY} from ${ORIGIN_REMOTE} ..."
git fetch "$ORIGIN_REMOTE" "$PRIMARY"
git checkout "$PRIMARY"
if ! git merge --ff-only "refs/remotes/${ORIGIN_REMOTE}/${PRIMARY}"; then
  echo "!! Local ${PRIMARY} has diverged from ${ORIGIN_REMOTE}/${PRIMARY}; reconcile that first." >&2
  exit 1
fi

echo ">> Merging upstream ${BRANCH} into ${PRIMARY} ..."
git merge --no-ff "refs/remotes/upstream/${BRANCH}"
git push "$ORIGIN_REMOTE" "$PRIMARY"
echo ">> Synced upstream ${BRANCH} into ${PRIMARY}."
