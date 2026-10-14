import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import test from 'node:test';
import { decideScopes, checkRequired } from './ci-scope.mjs';

const script = resolve('scripts/fork-sync.sh');
function fixture(mode) {
  const dir = mkdtempSync(join(tmpdir(), 'fork-sync-'));
  mkdirSync(join(dir, 'bin'));
  writeFileSync(join(dir, 'bin', 'gh'), `#!/usr/bin/env bash
set -eu
printf 'gh %s\\n' "$*" >> "$CALLS"
case "$*" in
  *issues?*)
    [[ "$MODE" != api-failure ]] || exit 1
    [[ "$MODE" != blocked ]] || echo '[fork-sync blocked] abc'
    ;;
  *'pulls?'*) [[ "$MODE" != open-pr ]] || echo 'https://github.com/example/pull/1' ;;
  *'state=all'*) [[ \"$MODE\" != closed-round ]] || echo 'https://github.com/example/pull/2' ;;
  'workflow run ci.yml'*) [[ "$MODE" != dispatch-failure ]] || exit 1 ;;
esac
exit 0
`, { mode: 0o755 });
  writeFileSync(join(dir, 'bin', 'git'), `#!/usr/bin/env bash
set -eu
printf 'git %s\\n' "$*" >> "$CALLS"
case "$*" in
  'rev-parse HEAD') echo base ;;
  'rev-parse upstream/main') echo upstream ;;
  'merge-base '*) [[ "$MODE" == no-op ]] ;;
  'ls-remote '*)
    [[ "$MODE" != git-api-failure ]] || exit 1
    [[ "$MODE" != existing ]] || echo 'sha refs/heads/candidate' ;;
  'merge --no-ff '*)
    echo 'merge diagnostic'
    [[ "$MODE" != conflict ]] || exit 1
    ;;
  'diff --name-only --diff-filter=U') echo 'server/pkg/agent/classify.go' ;;
  'diff --name-only '*'.github/workflows .github/actions '*) [[ \"$MODE\" != automation-change ]] || echo '.github/workflows/ci.yml' ;;
esac
`, { mode: 0o755 });
  const result = spawnSync('bash', [script], { cwd: dir, encoding: 'utf8', env: {
    ...process.env, PATH: `${dir}/bin:${process.env.PATH}`, MODE: mode,
    CALLS: join(dir, 'calls'), RUNNER_TEMP: dir, GH_REPO: 'example/repo',
    GH_TOKEN: 'test-not-a-credential', GITHUB_RUN_ID: '123',
  }});
  const calls = readFileSync(join(dir, 'calls'), 'utf8');
  return { dir, result, calls, cleanup: () => rmSync(dir, { recursive: true, force: true }) };
}
for (const mode of ['blocked', 'open-pr', 'no-op', 'existing', 'closed-round', 'api-failure', 'git-api-failure']) {
  test(`${mode} cannot publish or dispatch`, () => {
    const f = fixture(mode);
    try {
      assert.equal(f.result.status, mode.endsWith('api-failure') ? 1 : 0, f.result.stderr);
      assert.doesNotMatch(f.calls, /git push|gh pr create|gh workflow run/);
    } finally { f.cleanup(); }
  });
}
test('conflict diagnostics are captured before abort and a blocker is created', () => {
  const f = fixture('conflict');
  try {
    assert.equal(f.result.status, 1);
    assert.match(readFileSync(join(f.dir, 'fork-sync-diagnostics', 'merge.log'), 'utf8'), /merge diagnostic/);
    assert.match(readFileSync(join(f.dir, 'fork-sync-diagnostics', 'conflicted-files.txt'), 'utf8'), /classify.go/);
    assert.ok(f.calls.indexOf('git diff --cc') < f.calls.indexOf('git merge --abort'));
    assert.match(f.calls, /gh issue create.*\[fork-sync blocked\]/);
    assert.doesNotMatch(f.calls, /git push|gh pr create|gh workflow run/);
  } finally { f.cleanup(); }
});
test('clean merge pushes only candidate and dispatches both workflows on candidate ref', () => {
  const f = fixture('success');
  try {
    assert.equal(f.result.status, 0, f.result.stderr);
    assert.match(f.calls, /git push origin HEAD:refs\/heads\/sync\/upstream-upstream-onto-base/);
    assert.match(f.calls, /gh pr create .*--draft/);
    for (const workflow of ['ci.yml', 'mobile-verify.yml']) {
      assert.ok(f.calls.includes(`gh workflow run ${workflow} --repo example/repo --ref sync/upstream-upstream-onto-base`));
    }
    assert.doesNotMatch(f.calls, /push origin main|--force|pr merge/);
  } finally { f.cleanup(); }
});
test('dispatch failure fails round and creates manual blocker', () => {
  const f = fixture('dispatch-failure');
  try {
    assert.equal(f.result.status, 1);
    assert.match(f.calls, /gh issue create/);
  } finally { f.cleanup(); }
});
test('sync candidate PRs require full scopes', () => {
  assert.equal(decideScopes('pull_request', {}, 'sync/upstream-abc').full, 'true');
});
function requiredNeeds(event = 'workflow_dispatch') {
  return {
    changes: { result: 'success', outputs: decideScopes(event, {}, 'sync/upstream-abc') },
    ...Object.fromEntries(['frontend','backend','sqlc-check','windows-execenv','macos-runtime','installer','script-checks','image-budget']
      .map(job => [job, { result: job === 'image-budget' && event !== 'pull_request' ? 'skipped' : 'success' }])),
  };
}
test('required check rejects failed, cancelled, missing and unexpectedly skipped dependencies', () => {
  checkRequired(requiredNeeds(), 'workflow_dispatch');
  checkRequired(requiredNeeds('pull_request'), 'pull_request');
  for (const job of Object.keys(requiredNeeds())) {
    for (const result of ['failure', 'cancelled', 'skipped', undefined]) {
      if (job === 'image-budget' && result === 'skipped') continue;
      const needs = requiredNeeds();
      needs[job].result = result;
      assert.throws(() => checkRequired(needs, 'workflow_dispatch'));
    }
  }
  assert.throws(() => checkRequired({...requiredNeeds(), newJob: {result:'success'}}, 'workflow_dispatch'));
});

test('changed workflow definitions are quarantined before push or dispatch', () => {
  const f = fixture('automation-change');
  try {
    assert.equal(f.result.status, 1);
    assert.match(f.calls, /gh issue create/);
    assert.doesNotMatch(f.calls, /git push|gh pr create|gh workflow run/);
  } finally { f.cleanup(); }
});

test('candidate dispatch cannot skip its image budget', () => {
  const needs = requiredNeeds();
  assert.throws(() => checkRequired(needs, 'workflow_dispatch', 'sync/upstream-abc'));
  needs['image-budget'].result = 'success';
  checkRequired(needs, 'workflow_dispatch', 'sync/upstream-abc');
});
