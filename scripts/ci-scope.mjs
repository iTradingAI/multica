#!/usr/bin/env node
// Path filtering and aggregate gates share this fail-closed decision contract.
// The one deliberate opening: a hosted job GitHub abandoned without a runner
// never executed product code, so the gates tolerate that verdict loudly
// instead of reporting a regression that was never tested.
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

// GitHub marks a hosted job "abandoned" when no runner ever acquired it —
// most often macOS arm64 capacity. No product code ran, so failing the gate
// would report a regression that was never tested. Only that exact verdict is
// tolerated, and loudly: a warning annotation plus a job-summary entry name
// the coverage gap. Matrix legs abandoned under fail-fast still aggregate to
// failure/cancelled and fail as before.
const abandonedJobs = [];

function tolerateAbandoned(job) {
  abandonedJobs.push(job);
  const note = `${job} was never acquired by a hosted runner (abandoned); its scope was not verified in this run.`;
  console.log(`::warning title=${job} abandoned::${note}`);
  if (process.env.GITHUB_STEP_SUMMARY) {
    appendFileSync(process.env.GITHUB_STEP_SUMMARY, `> [!WARNING]\n> ${note}\n\n`);
  }
}

export function checkGate(needs, jobScopes) {
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
      tolerateAbandoned(job);
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

export function checkRequired(needs, event, branch = "") {
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
      tolerateAbandoned(job);
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
        checkRequired(JSON.parse(process.env.NEEDS_JSON), process.env.EVENT_NAME, process.env.CANDIDATE_BRANCH);
        break;
      case "gate": {
        checkGate(JSON.parse(process.env.NEEDS_JSON), JSON.parse(process.env.JOB_SCOPES));
        console.log(abandonedJobs.length
          ? `All executed CI jobs succeeded; unselected jobs were skipped; abandoned without a runner: ${abandonedJobs.join(", ")}.`
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
