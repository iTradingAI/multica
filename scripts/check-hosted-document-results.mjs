import { readFileSync } from "node:fs";

const expectedTitles = [
  "a host-authored wrapper reload is not reported as hostile navigation",
  "reports a first-line synchronous plugin error to the prearmed host",
];

function* specs(suites) {
  for (const suite of suites) {
    yield* suite.specs ?? [];
    yield* specs(suite.suites ?? []);
  }
}

const report = JSON.parse(readFileSync(process.argv[2], "utf8"));
const hostedSpecs = [...specs(report.suites)].filter(
  (spec) => spec.file === "plugin-surface-document.spec.ts" ||
    spec.file === "e2e/plugin-surface-document.spec.ts",
);
for (const title of expectedTitles) {
  const matches = hostedSpecs.filter((spec) => spec.title === title);
  const [spec] = matches;
  const [test] = spec?.tests ?? [];
  if (matches.length !== 1 || spec.tests.length !== 1 ||
      test.projectName !== "chromium" || test.expectedStatus !== "passed" ||
      test.results.length !== 1 || test.results[0].status !== "passed") {
    throw new Error(`Expected one passing Chromium attempt for "${title}"`);
  }
  console.log(`Chromium passed: ${title}`);
}
