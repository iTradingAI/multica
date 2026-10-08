#!/usr/bin/env bash
# This script runs from main, not from the unreviewed merge candidate.
set -euo pipefail
: "${GH_REPO:?}" "${GH_TOKEN:?}" "${RUNNER_TEMP:?}"
marker='[fork-sync blocked]'
run_url="https://github.com/$GH_REPO/actions/runs/$GITHUB_RUN_ID"
diag="$RUNNER_TEMP/fork-sync-diagnostics"
mkdir -p "$diag"

# Read current state directly, not the eventually consistent search index.
issue_titles=$(gh api --paginate "repos/$GH_REPO/issues?state=open&per_page=100" \
  --jq '.[] | select(.pull_request == null) | .title')
blocked=$(printf '%s\n' "$issue_titles" | grep -F "$marker" || true)
if [[ -n "$blocked" ]]; then
  echo "::notice::Sync paused. Resolve and close the fork-sync blocking issue first."
  exit 0
fi
open_candidates=$(gh api --paginate "repos/$GH_REPO/pulls?state=open&base=main&per_page=100" \
  --jq '.[] | select(.head.ref | startswith("sync/upstream-")) | .html_url')
if [[ -n "$open_candidates" ]]; then
  echo "::notice::Sync paused while a candidate awaits manual review: $open_candidates"
  exit 0
fi

git remote add upstream https://github.com/multica-ai/multica.git
git fetch --no-tags upstream main
base=$(git rev-parse HEAD)
upstream=$(git rev-parse upstream/main)
printf 'base=%s\nupstream=%s\nrun=%s\n' "$base" "$upstream" "$run_url" > "$diag/round.txt"
if git merge-base --is-ancestor "$upstream" "$base"; then
  echo 'main already contains upstream/main.'
  exit 0
fi
branch="sync/upstream-$upstream-onto-$base"
remote_round=$(git ls-remote --heads origin "refs/heads/$branch")
# Retain the no-retry decision even if a maintainer deleted a closed PR's branch.
prior_round=$(gh api --method GET --paginate "repos/$GH_REPO/pulls" \
  -f state=all -f "head=${GH_REPO%%/*}:$branch" -f per_page=100 --jq '.[].html_url')
if [[ -n "$remote_round" || -n "$prior_round" ]]; then
  echo '::notice::This exact round was already proposed. Inspect its PR/run; no blind retry.'
  exit 0
fi

block_round() {
  local reason=$1
  cat > "$diag/blocker.md" <<BODY
Automated upstream sync is paused: $reason

Base: $base
Upstream: $upstream
Candidate branch: $branch
Run and diagnostic artifact (30-day retention): $run_url

Resolve this round manually, preserve downstream behavior, and validate the resulting SHA with CI and Mobile Verify. Do not use blanket ours/theirs or force-push main. Close this issue only when the blocker is resolved; scheduled sync will remain paused while it is open.
BODY
  gh issue create --repo "$GH_REPO" --title "$marker $upstream" --body-file "$diag/blocker.md"
}

git config user.name 'fork-sync[bot]'
git config user.email 'actions@github.com'
git switch -c "$branch"
if ! git merge --no-ff --no-edit "$upstream" > "$diag/merge.log" 2>&1; then
  git status --porcelain=v1 > "$diag/status.txt"
  git diff --name-only --diff-filter=U > "$diag/conflicted-files.txt"
  git diff --cc > "$diag/conflicts.diff"
  git ls-files --unmerged > "$diag/unmerged-index.txt"
  git merge --abort
  block_round 'merge conflict (no candidate was pushed)'
  echo '::error::Upstream merge conflicted. Diagnostics preserved; manual resolution required.'
  exit 1
fi
candidate=$(git rev-parse HEAD)
printf 'candidate=%s\n' "$candidate" >> "$diag/round.txt"
# A dispatch uses the workflow definition from its target ref. Never execute
# changed automation with a token before a maintainer reviews that change.
git diff --name-only "$base" "$candidate" -- .github/workflows .github/actions .github/ci-paths.json scripts/ci-scope.mjs scripts/fork-sync.sh > "$diag/automation-changes.txt"
if [[ -s "$diag/automation-changes.txt" ]]; then
  git diff "$base" "$candidate" -- .github/workflows .github/actions .github/ci-paths.json scripts/ci-scope.mjs scripts/fork-sync.sh > "$diag/automation.diff"
  block_round 'upstream changes workflow/action definitions; review automation before publishing or dispatching the candidate'
  echo '::error::Automation changes require manual review. Candidate was not pushed.'
  exit 1
fi
git push origin "HEAD:refs/heads/$branch"
cat > "$diag/pr.md" <<BODY
## Summary
Propose upstream $upstream merged into main at $base without overwriting downstream changes.

## Validation
Candidate SHA: $candidate
CI and Mobile Verify are explicitly dispatched on this fixed candidate branch. Check that both runs report this exact head SHA and pass. GITHUB_TOKEN pushes do not trigger push CI; a PR alone is not validation. Any PR-run approval shown by GitHub must be handled by a maintainer.

## Manual review required
Review downstream compatibility and workflow changes, mark ready, and merge manually only after required checks pass and main is up to date. No auto-merge is enabled. If this PR is closed unmerged, this exact round will not be recreated.

Sync run: $run_url
BODY
if ! gh pr create --repo "$GH_REPO" --base main --head "$branch" --draft \
  --title "chore: sync upstream ${upstream:0:12}" --body-file "$diag/pr.md"; then
  block_round 'candidate pushed but PR creation failed'
  exit 1
fi
# workflow_dispatch is explicitly exempt from GITHUB_TOKEN recursion suppression.
# Dispatch to the candidate ref, not main + an alternate checkout SHA: GitHub
# must attach the resulting check suites to the candidate commit itself.
for workflow in ci.yml mobile-verify.yml; do
  if ! gh workflow run "$workflow" --repo "$GH_REPO" --ref "$branch"; then
    block_round "candidate created but $workflow dispatch failed"
    exit 1
  fi
done
printf 'Candidate %s explicitly dispatched; review the draft PR manually.\n' "$candidate"
