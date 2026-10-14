# Safe upstream sync (downstream only)

## Behavior

The daily 03:17 UTC job proposes a merge of `multica-ai/multica:main` into the current downstream `main`. It never pushes main, force-pushes, or merges a PR. It creates a draft PR on a uniquely named `sync/upstream-<upstream SHA>-onto-<base SHA>` branch. It does not update that branch automatically. Treat it as a fixed candidate; GitHub does not make it immutable against human pushes.

One open sync PR pauses new rounds. A merge conflict saves logs, unmerged paths/index, and the combined conflict diff to the run's `fork-sync-*` artifact (30 days), opens a `[fork-sync blocked]` issue, and stops. Any open issue with that marker pauses all later rounds until a maintainer resolves it and closes the issue. A closed, unmerged round is not blindly recreated; a changed main/upstream SHA creates a different round. Do not close a blocker merely because upstream advanced: resolve the underlying conflict or inspect the new pair first.

Workflow/action definitions and the sync/CI gate controls are quarantined before any candidate push when changed upstream. Review these manually before publishing or executing them. The trusted proposal script is copied out of the checkout before the merge, so a merge cannot replace its running instructions. No PAT, new credential, repository secret, or persistent grant is added.

## Candidate validation

A `GITHUB_TOKEN` push does not start push CI. The proposal explicitly calls `workflow_dispatch` for both `ci.yml` and `mobile-verify.yml` using the candidate branch as the dispatch ref. GitHub thus associates both runs with the candidate head SHA, and all checkouts pin `github.sha`; dispatching on main and checking out a different SHA would not be equivalent. Sync PRs and dispatches select full CI, including platform checks. Mobile always runs so its required check is not left pending on unrelated PRs. Existing daily full CI remains available.

GitHub may additionally require approval for token-created PR workflow runs. A maintainer handles that approval. Dispatch failures open a blocker instead of claiming success. A successful dispatch only means validation was requested, not that it passed. Compare each run's `head_sha` with the PR's current head before accepting its green result.

## Rollout (administrator action required)

1. Review and manually merge the implementation PR after its CI and Mobile Verify checks pass. It is a draft; no automatic merge is configured. Until this lands on main, the old direct-sync workflow is still active and Mobile Verify has no workflow_dispatch trigger; candidate dispatch cannot be bootstrapped against old main. If rollout spans the next 03:17 UTC run, disable the old Fork sync workflow in Actions temporarily, then re-enable after deployment.
2. The proposal job needs the existing per-run `GITHUB_TOKEN` permissions `contents: write`, `pull-requests: write`, `issues: write`, and `actions: write`. The last three enable draft PRs, persistent conflict blockers, and explicit dispatch. All validation workflows default to read-only, with checkout credentials disabled. Review this permission change before enabling the workflow. In Settings > Actions > General, enable **Allow GitHub Actions to create and approve pull requests** if necessary; the workflow only creates PRs and never approves them. This repository setting requires administrator authorization and is not changed by this PR.
3. Under Settings > Branches, create protection for **main** (or equivalent active ruleset), with:
   - Require a pull request before merging; require **1 approval**, dismiss stale approvals, and require approval of the most recent reviewable push. A separate reviewer is needed for human-authored PRs; the owner can review bot-authored sync PRs.
   - Require status checks: **ci-required** and **mobile**, sourced from GitHub Actions. Require branches to be up to date before merging (strict). This rejects a green result based on stale main; update the candidate with main and rerun both pipelines on the new head.
   - Require conversation resolution; do not allow force pushes or deletions.
   - Include administrators / disallow bypass. Do not give the sync actor a bypass allowance.
   - Disable repository auto-merge in Settings > General to enforce the manual-merge policy for all future sync PRs. This is an admin setting, not something this workflow toggles.
4. Verify the protection/ruleset via API or UI, rather than assuming the YAML enforces it. Check that an out-of-date candidate and one with a failed/missing `ci-required` or `mobile` check cannot merge. Run Fork sync manually from main. If already up to date, it should exit without a branch or PR; test the candidate path when an upstream change is available.

The implementation PR alone does not activate protection, change Actions settings, or prove the scheduled proposal/dispatch path has run. Do not describe rollout as complete until these steps are verified.

## Manual recovery

Read the blocking issue and the linked run artifact first. Preserve downstream behavior; never resolve the entire merge with `ours`/`theirs` and never force-push main.

- Conflict or quarantined automation: reproduce the listed base/upstream SHAs on a new review branch, resolve/review the changes, create a draft PR, and explicitly run both validation workflows on that branch. Because the automation blocks before pushing, there is no remote candidate from this failed merge to overwrite.
- PR-creation failure after candidate push: inspect the recorded branch SHA, then create the draft PR manually. Do not delete/recreate a branch merely to trigger automation.
- Dispatch failure or human update of a candidate branch: after reviewing workflow definitions and confirming the current head, run:

  ```sh
  gh workflow run ci.yml --repo iTradingAI/multica --ref '<candidate branch>'
  gh workflow run mobile-verify.yml --repo iTradingAI/multica --ref '<candidate branch>'
  gh run list --repo iTradingAI/multica --branch '<candidate branch>' --event workflow_dispatch
  ```

  Verify both runs' head SHAs equal `gh pr view <number> --repo iTradingAI/multica --json headRefOid --jq .headRefOid`, then wait for all checks. Do not reuse green checks from the old SHA.
- Main advanced: merge current main into the review branch without rewriting history, review any new automation changes, then rerun both validations. Protection must enforce strict up-to-date checks.
- Close the `[fork-sync blocked]` issue only once the round is resolved or a reviewed replacement is underway. Keep the diagnostic evidence if needed beyond artifact retention. Mark a candidate ready and merge only by a human after approval and required checks.

## Local checks

```sh
node --test scripts/ci-scope.test.mjs scripts/check-image-budget.test.mjs scripts/fork-sync.test.mjs
bash -n scripts/fork-sync.sh
git diff --check
```

The shell tests use stubbed Git/GitHub commands to cover no-op, blocking issue, existing PR, duplicate round, API failure, conflict diagnostics, automation quarantine, clean publication, and dispatch failure. They do not claim a live GitHub dispatch or branch-protection integration test.
