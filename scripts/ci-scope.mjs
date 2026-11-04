#!/usr/bin/env node
// Path filtering and aggregate gates share this fail-closed decision contract.
// The one deliberate opening: a hosted job GitHub abandoned without a runner —
// or cancelled before a single step ran — never executed product code, so the
// gates tolerate that verdict loudly instead of reporting a regression that
// was never tested.
import { appendFileSync, readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";

export const filters = JSON.parse(
  readFileSync(new URL("../.github/ci-paths.json", import.meta.url), "utf8"),
);

export function decideScopes(event, filtered, branch = "") {
  const full = event === "schedule" || event === "workflow_dispatch" || branch.startsWith("sync/upstream-");
  if (!full && event !== "push" && event !== "pull_request") {
    throw new Error(`Unsupported CI event: ${event}`);
  }
  const outputs = { full: String(full) };
  for (const scope of Object.keys(filters)) {
    if (!full && filtered[scope] !== "true" && filtered[scope] !== "false") {
      throw new Error(`Missing or invalid path-filter result: ${scope}`);
    }
    outputs[scope] = full ? "true" : filtered[scope];
  }
  // Product builds already install dependencies and run the shared checks.
  // Only allocate a separate runner when quality is the sole frontend work.
  outputs.quality_only = String(outputs.quality === "true" && outputs.frontend === "false");
  return outputs;
}

// Jobs tolerated as never-run in this process, for the gate's final message.
const notRunJobs = [];

function tolerateNotRun(job, verdict) {
  notRunJobs.push(`${job} (${verdict})`);
  const note = `${job} never ran (${verdict}); no hosted runner executed it, so its scope was not verified in this run.`;
  console.log(`::warning title=${job} ${verdict}::${note}`);
  if (process.env.GITHUB_STEP_SUMMARY) {
    appendFileSync(process.env.GITHUB_STEP_SUMMARY, `> [!WARNING]\n> ${note}\n\n`);
  }
}

// "abandoned" itself means GitHub never acquired a runner for the job. A
// "cancelled" verdict alone is ambiguous — user cancel, fail-fast cascade, or
// capacity — so it is tolerated only with API proof that the job completed as
// cancelled without executing a single step. Matrix legs are matched too; any
// executed step, any other conclusion, or any doubt fails closed.
export async function neverRanCancelled(job) {
  const { GITHUB_API_URL = "https://api.github.com", GITHUB_REPOSITORY, GITHUB_RUN_ID, GITHUB_TOKEN } = process.env;
  if (!GITHUB_REPOSITORY || !GITHUB_RUN_ID || !GITHUB_TOKEN) return false;
  const response = await fetch(
    `${GITHUB_API_URL}/repos/${GITHUB_REPOSITORY}/actions/runs/${GITHUB_RUN_ID}/jobs?per_page=100`,
    { headers: { Authorization: `Bearer ${GITHUB_TOKEN}`, Accept: "application/vnd.github+json" } },
  );
  if (!response.ok) return false;
  const { jobs = [] } = await response.json();
  const legs = jobs.filter((candidate) => candidate.name === job || candidate.name.startsWith(`${job} (`));
  return legs.length > 0 && legs.every((leg) =>
    leg.status === "completed" && leg.conclusion === "cancelled" && !(leg.steps?.length > 0));
}

export async function checkGate(needs, jobScopes, verifyCancelled = neverRanCancelled) {
  if (needs.changes?.result !== "success") {
    throw new Error("Path filtering did not succeed");
  }
  for (const [job, scope] of Object.entries(jobScopes)) {
    const selected = needs.changes.outputs?.[scope];
    if (selected !== "true" && selected !== "false") {
      throw new Error(`Missing or invalid scope: ${scope}`);
    }
    const expected = selected === "true" ? "success" : "skipped";
    const result = needs[job]?.result;
    if (expected === "success" && result === "abandoned") {
      tolerateNotRun(job, "abandoned");
      continue;
    }
    if (expected === "success" && result === "cancelled" && (await verifyCancelled(job))) {
      tolerateNotRun(job, "cancelled");
      continue;
    }
    if (result !== expected) {
      throw new Error(`${job}: expected ${expected}, got ${result ?? "missing"}`);
    }
  }
  // A newly added dependency must have a declared scope before the gate can pass.
  for (const job of Object.keys(needs)) {
    if (job !== "changes" && !Object.hasOwn(jobScopes, job)) {
      throw new Error(`Unchecked dependency: ${job}`);
    }
  }
}

export async function checkRequired(needs, event, branch = "", verifyCancelled = neverRanCancelled) {
  for (const job of ["changes", "frontend", "backend"]) {
    if (needs[job]?.result !== "success") throw new Error(`${job} did not succeed`);
  }
  const scopes = {
    "sqlc-check": "sqlc", "windows-execenv": "runtime", "macos-runtime": "full",
    installer: "installer", "script-checks": "scripts", "image-budget": "images",
  };
  for (const [job, scope] of Object.entries(scopes)) {
    const selected = needs.changes.outputs?.[scope];
    if (!["true", "false"].includes(selected)) throw new Error(`Invalid scope: ${scope}`);
    const expected = selected === "true" && (job !== "image-budget" || event === "pull_request" || branch.startsWith("sync/upstream-"))
      ? "success" : "skipped";
    const result = needs[job]?.result;
    if (expected === "success" && result === "abandoned") {
      tolerateNotRun(job, "abandoned");
      continue;
    }
    if (expected === "success" && result === "cancelled" && (await verifyCancelled(job))) {
      tolerateNotRun(job, "cancelled");
      continue;
    }
    if (result !== expected) throw new Error(`${job}: expected ${expected}`);
  }
  const allowed = new Set(["changes", "frontend", "backend", ...Object.keys(scopes)]);
  for (const job of Object.keys(needs)) {
    if (!allowed.has(job)) throw new Error(`Unchecked dependency: ${job}`);
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    switch (process.argv[2]) {
      case "decide": {
        const outputs = decideScopes(process.env.EVENT_NAME, JSON.parse(process.env.FILTER_RESULTS), process.env.CANDIDATE_BRANCH);
        appendFileSync(process.env.GITHUB_OUTPUT,
          Object.entries(outputs).map(([key, value]) => `${key}=${value}\n`).join(""));
        break;
      }
      case "required":
        await checkRequired(JSON.parse(process.env.NEEDS_JSON), process.env.EVENT_NAME, process.env.CANDIDATE_BRANCH);
        break;
      case "gate": {
        await checkGate(JSON.parse(process.env.NEEDS_JSON), JSON.parse(process.env.JOB_SCOPES));
        console.log(notRunJobs.length
          ? `All executed CI jobs succeeded; unselected jobs were skipped; never run: ${notRunJobs.join(", ")}.`
          : "All selected CI jobs succeeded; only unselected jobs were skipped.");
        break;
      }
      default:
        throw new Error("Usage: node scripts/ci-scope.mjs decide|gate");
    }
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
