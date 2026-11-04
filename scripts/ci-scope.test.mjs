import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, matchesGlob } from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { checkGate, checkRequired, decideScopes, filters, neverRanCancelled } from "./ci-scope.mjs";

// The filters use only literal paths, * and **; Node's matcher exercises those
// against realistic changes, including files removed or renamed by a PR.
function filterFiles(files) {
  return Object.fromEntries(Object.entries(filters).map(([scope, patterns]) => [
    scope, String(files.some((file) => patterns.some((pattern) => matchesGlob(file, pattern)))),
  ]));
}

for (const [name, files, selected] of [
  ["readme only", ["README.md"], []],
  ["docs only", ["apps/docs/content/docs/guide.mdx"], ["quality"]],
  ["web changelog", ["apps/web/features/landing/i18n/en.ts"], ["frontend", "quality"]],
  ["UI Lab", ["apps/ui-lab/src/app.tsx"], ["frontend", "quality"]],
  ["mobile UI", ["apps/mobile/app/index.tsx"], ["quality"]],
  ["migration only", ["server/migrations/999_example.up.sql"], ["backend", "sqlc"]],
  ["agent process code", ["server/pkg/agent/cursor_background.go"], ["backend", "runtime"]],
  ["daemon dependency", ["server/internal/skill/service.go"], ["backend", "runtime"]],
  ["native test compilation dependency", ["server/pkg/db/generated/issues.sql.go"], ["backend", "sqlc", "runtime"]],
  ["Go dependencies", ["server/go.mod", "server/go.sum"], ["backend", "runtime"]],
  ["Helm only", ["deploy/helm/multica/templates/deployment.yaml"], ["scripts"]],
  ["container entrypoint", ["docker/entrypoint.sh"], ["scripts"]],
  ["selfhost config", [".env.example"], ["scripts", "installer"]],
  ["shell installer", ["scripts/install.sh"], ["scripts", "installer"]],
  ["PowerShell installer", ["scripts/install.ps1.test.ps1"], ["installer"]],
  ["cleanup script", ["scripts/drop-database.sh"], ["scripts"]],
  ["check script regression tests", ["scripts/check.test.sh"], ["scripts"]],
  ["performance harness", ["scripts/perf-compare.test.sh"], ["scripts"]],
  ["reserved slug source", ["server/internal/handler/reserved_slugs.json"], ["backend", "runtime", "scripts"]],
  ["reserved slug output", ["packages/core/paths/reserved-slugs.ts"], ["frontend", "quality", "scripts"]],
  ["integration key gates", ["server/cmd/server/router.go"], ["backend", "runtime", "scripts"]],
  ["cross-module runtime contract", ["packages/core/runtimes/cli-version.ts"], ["frontend", "backend", "runtime", "quality"]],
  ["lockfile", ["pnpm-lock.yaml"], ["frontend", "quality"]],
  ["workspace package patch", ["patches/example.patch"], ["frontend", "quality"]],
  ["mobile package patch", ["apps/mobile/patches/example.patch"], ["quality"]],
  ["radius policy", ["scripts/check-ui-radius-tokens.mjs"], ["quality"]],
  ["shared quality action", [".github/actions/frontend-quality/action.yml"], ["frontend", "quality"]],
  ["new bitmap", ["apps/web/public/hero.png"], ["frontend", "quality", "images"]],
  ["mixed docs and migration", ["apps/docs/content/docs/guide.mdx", "server/migrations/999_example.up.sql"], ["quality", "backend", "sqlc"]],
  ["CI configuration", [".github/ci-paths.json"], Object.keys(filters)],
]) {
  test(`PR and main select only affected scopes: ${name}`, () => {
    for (const event of ["pull_request", "push"]) {
      const outputs = decideScopes(event, filterFiles(files));
      assert.equal(outputs.full, "false");
      assert.deepEqual(Object.keys(filters).filter((scope) => outputs[scope] === "true").sort(),
        [...selected].sort());
      assert.equal(outputs.quality_only, String(selected.includes("quality") && !selected.includes("frontend")));
    }
  });
}

test("scheduled and manual runs select every scope without a path-filter result", () => {
  for (const event of ["schedule", "workflow_dispatch"]) {
    const { quality_only, ...scopes } = decideScopes(event, {});
    assert.ok(Object.values(scopes).every((value) => value === "true"));
    assert.equal(quality_only, "false", "the full frontend build owns quality checks");
  }
});

test("missing, malformed and unsupported filter results fail closed", () => {
  for (const value of [undefined, "", "unknown", true]) {
    assert.throws(() => decideScopes("push", { ...filterFiles([]), backend: value }), /backend/);
  }
  assert.throws(() => decideScopes("unknown", filterFiles([])), /Unsupported/);
});

// Read the production wiring without installing workspace dependencies in the
// lightweight changes job. These fields deliberately use single-line syntax;
// unsupported formatting fails the assertions instead of being silently ignored.
const workflow = readFileSync(new URL("../.github/workflows/ci.yml", import.meta.url), "utf8");
const jobSource = workflow.slice(workflow.indexOf("\njobs:\n") + "\njobs:\n".length);
const headings = [...jobSource.matchAll(/^  ([\w-]+):$/gm)];
const jobs = Object.fromEntries(headings.map((match, index) => [
  match[1], jobSource.slice(match.index, headings[index + 1]?.index),
]));
function field(source, pattern) {
  const match = source.match(pattern);
  assert.ok(match, `Missing production field: ${pattern}`);
  return match[1];
}
function productionMapping(gate) {
  return JSON.parse(field(jobs[gate], /^          JOB_SCOPES: '(.+)'$/m));
}
function productionNeeds(gate, outputs) {
  const dependencies = field(jobs[gate], /^    needs: \[(.+)\]$/m).split(", ");
  return Object.fromEntries(dependencies.map((job) => {
    if (job === "changes") return [job, { result: "success", outputs }];
    const scope = field(jobs[job], /^    if: \$\{\{ needs\.changes\.outputs\.(\w+) == 'true' \}\}$/m);
    return [job, { result: outputs[scope] === "true" ? "success" : "skipped" }];
  }));
}

for (const gate of ["frontend", "backend"]) {
  test(`production ${gate} gate matches every dependency, condition and scope output`, async () => {
    const mapping = productionMapping(gate);
    assert.match(jobs[gate], /^    if: \$\{\{ !cancelled\(\) \}\}$/m);
    assert.match(jobs[gate], /^          NEEDS_JSON: \$\{\{ toJSON\(needs\) \}\}$/m);
    assert.match(jobs[gate], /^          GITHUB_TOKEN: \$\{\{ secrets\.GITHUB_TOKEN \}\}$/m);
    assert.match(jobs[gate], /^    permissions:\n      contents: read\n(?:      # .*\n)?      actions: read\n/m);
    assert.match(jobs[gate], /^        run: node scripts\/ci-scope\.mjs gate$/m);
    for (const scope of Object.values(mapping)) {
      assert.ok(jobs.changes.includes(`      ${scope}: \${{ steps.decide.outputs.${scope} }}`));
    }
    const scopes = Object.keys(filters);
    // Exercise mixed scopes too: installers and quality may run without the
    // corresponding product backend/frontend suite being selected.
    for (let mask = 0; mask < 2 ** scopes.length; mask++) {
      const filtered = Object.fromEntries(scopes.map((scope, bit) => [scope, String(Boolean(mask & (1 << bit)))]));
      const outputs = decideScopes("pull_request", filtered);
      await checkGate(productionNeeds(gate, outputs), mapping);
    }
    for (const event of ["schedule", "workflow_dispatch"]) {
      await checkGate(productionNeeds(gate, decideScopes(event, {})), mapping);
    }
  });

  test(`production ${gate} gate rejects unsuccessful or missing selected jobs`, async () => {
    const mapping = productionMapping(gate);
    for (const job of Object.keys(mapping)) {
      // A docs-only run selects the standalone quality runner; a full run
      // selects every other dependency, including the installer matrix.
      const outputs = job === "frontend-quality"
        ? decideScopes("pull_request", filterFiles(["apps/docs/content/docs/guide.mdx"]))
        : decideScopes("workflow_dispatch", {});
      for (const result of ["failure", "cancelled", "skipped", undefined]) {
        const input = productionNeeds(gate, outputs);
        assert.equal(input[job].result, "success");
        input[job].result = result;
        // "cancelled" only passes with API proof of zero steps; with the
        // default verifier (no token, no network) every verdict fails closed.
        await assert.rejects(() => checkGate(input, mapping), new RegExp(job));
      }
      const missing = productionNeeds(gate, outputs);
      delete missing[job];
      await assert.rejects(() => checkGate(missing, mapping), new RegExp(job));
    }
  });

  test(`production ${gate} gate rejects invalid filtering and unchecked dependencies`, async () => {
    const mapping = productionMapping(gate);
    const unselected = () => productionNeeds(gate, decideScopes("pull_request", filterFiles([])));
    for (const result of ["failure", "cancelled", "skipped", undefined]) {
      const input = unselected();
      input.changes.result = result;
      await assert.rejects(() => checkGate(input, mapping), /Path filtering/);
    }
    for (const [job, scope] of Object.entries(mapping)) {
      const failed = unselected();
      failed[job].result = "failure";
      await assert.rejects(() => checkGate(failed, mapping), new RegExp(job));
      for (const value of [undefined, "", "unknown"]) {
        const invalid = unselected();
        invalid.changes.outputs[scope] = value;
        await assert.rejects(() => checkGate(invalid, mapping), /scope/);
      }
    }
    const extra = unselected();
    extra.extra = { result: "failure" };
    await assert.rejects(() => checkGate(extra, mapping), /Unchecked dependency/);
  });

  test(`production ${gate} gate tolerates a job abandoned without a hosted runner`, async () => {
    const mapping = productionMapping(gate);
    for (const job of Object.keys(mapping)) {
      // Same selections as the rejection test: docs-only selects the standalone
      // quality runner; a full run selects every other dependency.
      const outputs = job === "frontend-quality"
        ? decideScopes("pull_request", filterFiles(["apps/docs/content/docs/guide.mdx"]))
        : decideScopes("workflow_dispatch", {});
      const input = productionNeeds(gate, outputs);
      assert.equal(input[job].result, "success");
      input[job].result = "abandoned";
      await checkGate(input, mapping);
    }
  });

  test(`production ${gate} gate tolerates a cancelled job only with proof it never ran`, async () => {
    const mapping = productionMapping(gate);
    for (const job of Object.keys(mapping)) {
      const outputs = job === "frontend-quality"
        ? decideScopes("pull_request", filterFiles(["apps/docs/content/docs/guide.mdx"]))
        : decideScopes("workflow_dispatch", {});
      const input = productionNeeds(gate, outputs);
      assert.equal(input[job].result, "success");
      input[job].result = "cancelled";
      // Only the cancelled verdict consults the verifier; tolerance requires
      // its zero-steps proof, and its refusal must still fail the gate.
      await checkGate(input, mapping, async (queried) => {
        assert.equal(queried, job);
        return true;
      });
      await assert.rejects(() => checkGate(input, mapping, async () => false), new RegExp(job));
    }
  });
}

test("the required gate tolerates abandoned jobs and rejects every other verdict", async () => {
  const outputs = { full: "true", ...Object.fromEntries(Object.keys(filters).map((scope) => [scope, "true"])) };
  // ci-required's dependencies on a scheduled full run; image-budget is
  // main-branch-skipped because only PRs and sync branches have descriptions.
  const base = {
    changes: { result: "success", outputs },
    frontend: { result: "success" },
    backend: { result: "success" },
    "sqlc-check": { result: "success" },
    "windows-execenv": { result: "success" },
    "macos-runtime": { result: "success" },
    installer: { result: "success" },
    "script-checks": { result: "success" },
    "image-budget": { result: "skipped" },
  };
  assert.match(jobs["ci-required"], /^          GITHUB_TOKEN: \$\{\{ secrets\.GITHUB_TOKEN \}\}$/m);
  assert.match(jobs["ci-required"], /^    permissions:\n      contents: read\n(?:      # .*\n)?      actions: read\n/m);
  for (const job of ["macos-runtime", "windows-execenv", "installer", "script-checks", "sqlc-check"]) {
    const abandoned = { ...base, [job]: { result: "abandoned" } };
    await checkRequired(abandoned, "schedule", "");
    const cancelled = { ...base, [job]: { result: "cancelled" } };
    await checkRequired(cancelled, "schedule", "", async () => true);
    await assert.rejects(() => checkRequired(cancelled, "schedule", ""), new RegExp(job));
    for (const result of ["failure", "timed_out", undefined]) {
      const failed = { ...base, [job]: { result } };
      await assert.rejects(() => checkRequired(failed, "schedule", ""), new RegExp(job));
    }
  }
});

test("cancelled tolerance needs API proof that nothing executed, else fails closed", async (t) => {
  const saved = ["GITHUB_REPOSITORY", "GITHUB_RUN_ID", "GITHUB_TOKEN"].map((key) => [key, process.env[key]]);
  const realFetch = globalThis.fetch;
  t.after(() => {
    globalThis.fetch = realFetch;
    for (const [key, value] of saved) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
  });
  process.env.GITHUB_REPOSITORY = "octocat/demo";
  process.env.GITHUB_RUN_ID = "42";
  process.env.GITHUB_TOKEN = "test-token";
  const respondWith = (json, ok = true) => {
    globalThis.fetch = async () => ({ ok, json: async () => json });
  };
  const leg = (name, conclusion, steps) => ({ name, status: "completed", conclusion, steps });
  respondWith({ jobs: [leg("macos-runtime", "cancelled", [])] });
  assert.equal(await neverRanCancelled("macos-runtime"), true);
  // A recorded step means product code ran; the verdict is no longer pure
  // infrastructure and must fail the gate.
  respondWith({ jobs: [leg("macos-runtime", "cancelled", [{ name: "Set up job", conclusion: "success" }])] });
  assert.equal(await neverRanCancelled("macos-runtime"), false);
  respondWith({ jobs: [leg("macos-runtime", "failure", [])] });
  assert.equal(await neverRanCancelled("macos-runtime"), false);
  respondWith({ jobs: [leg("unrelated-job", "cancelled", [])] });
  assert.equal(await neverRanCancelled("macos-runtime"), false);
  // Matrix legs count as the job only when every leg stayed unexecuted.
  respondWith({ jobs: [leg("installer (ubuntu-latest)", "cancelled", []), leg("installer (macos-latest)", "cancelled", [])] });
  assert.equal(await neverRanCancelled("installer"), true);
  respondWith({ jobs: [leg("installer (ubuntu-latest)", "cancelled", [{ name: "Set up job", conclusion: "success" }]), leg("installer (macos-latest)", "cancelled", [])] });
  assert.equal(await neverRanCancelled("installer"), false);
  // API errors and missing wiring fail closed.
  respondWith({}, false);
  assert.equal(await neverRanCancelled("macos-runtime"), false);
  delete process.env.GITHUB_TOKEN;
  respondWith({ jobs: [leg("macos-runtime", "cancelled", [])] });
  assert.equal(await neverRanCancelled("macos-runtime"), false);
});

test("the backend gate owns the three-platform installer matrix", () => {
  assert.equal(productionMapping("backend").installer, "installer");
  assert.match(jobs.installer, /^        os: \[ubuntu-latest, macos-latest, windows-latest\]$/m);
  assert.doesNotMatch(jobs.installer, /continue-on-error:/);
});

test("quality checks have exactly one runner and reuse the product build install", () => {
  const invocation = "uses: ./.github/actions/frontend-quality";
  const owners = Object.entries(jobs).filter(([, source]) => source.includes(invocation)).map(([job]) => job);
  assert.deepEqual(owners.sort(), ["frontend-build", "frontend-quality"]);
  assert.equal(productionMapping("frontend")["frontend-quality"], "quality_only");
  assert.match(jobs["frontend-build"], /      - name: Check frontend quality\n        uses: \.\/\.github\/actions\/frontend-quality\n/);
  for (const frontend of ["true", "false"]) {
    for (const quality of ["true", "false"]) {
      const outputs = decideScopes("pull_request", { ...filterFiles([]), frontend, quality });
      const results = productionNeeds("frontend", outputs);
      const runners = owners.filter((job) => results[job].result === "success");
      assert.equal(runners.length, frontend === "true" || quality === "true" ? 1 : 0);
    }
  }
  const action = readFileSync(new URL("../.github/actions/frontend-quality/action.yml", import.meta.url), "utf8");
  assert.match(action, /run: pnpm knip/);
  assert.match(action, /continue-on-error: true/);
  assert.doesNotMatch(action, /pnpm install/);
});

test("the CLI writes real Actions outputs and exits nonzero on a failed gate", (t) => {
  const dir = mkdtempSync(join(tmpdir(), "ci-scope-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const output = join(dir, "output");
  const script = fileURLToPath(new URL("./ci-scope.mjs", import.meta.url));
  const run = spawnSync(process.execPath, [script, "decide"], {
    env: { ...process.env, EVENT_NAME: "push", FILTER_RESULTS: JSON.stringify(filterFiles(["README.md"])), GITHUB_OUTPUT: output },
    encoding: "utf8",
  });
  assert.equal(run.status, 0, run.stderr);
  assert.match(readFileSync(output, "utf8"), /^backend=false$/m);
  const failed = productionNeeds("backend", decideScopes("workflow_dispatch", {}));
  failed["backend-tests"].result = "failure";
  const gate = spawnSync(process.execPath, [script, "gate"], {
    env: { ...process.env, NEEDS_JSON: JSON.stringify(failed), JOB_SCOPES: JSON.stringify(productionMapping("backend")) },
    encoding: "utf8",
  });
  assert.equal(gate.status, 1);
  assert.match(gate.stderr, /backend-tests/);
  const abandoned = productionNeeds("backend", decideScopes("workflow_dispatch", {}));
  abandoned["macos-runtime"].result = "abandoned";
  const tolerated = spawnSync(process.execPath, [script, "gate"], {
    env: { ...process.env, NEEDS_JSON: JSON.stringify(abandoned), JOB_SCOPES: JSON.stringify(productionMapping("backend")) },
    encoding: "utf8",
  });
  assert.equal(tolerated.status, 0, tolerated.stderr);
  assert.match(tolerated.stdout, /::warning title=macos-runtime abandoned::/);
  assert.match(tolerated.stdout, /never run: macos-runtime \(abandoned\)/);
});
