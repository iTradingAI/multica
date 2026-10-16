#!/usr/bin/env node
/* global window */
// Explicit GUI smoke check; never part of the default test suite.
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtemp, readFile, writeFile } from "node:fs/promises";
import { createServer } from "node:http";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";
import electronEnv from "./electron-env.cjs";

const here = dirname(fileURLToPath(import.meta.url));
const root = resolve(here, "../../..");
const require = createRequire(import.meta.url);
const { _electron } = createRequire(join(root, "package.json"))("@playwright/test");
const executablePath = require("electron");
const entry = resolve(process.argv[2] || join(here, "fixtures/electron.cjs"));
const output = process.argv[3];
const profile = await mkdtemp(join(tmpdir(), "multica-electron-harness-"));
const polluted = { ...process.env, ELECTRON_RUN_AS_NODE: "1" };
const failure = spawnSync(executablePath, [entry], {
  env: polluted, encoding: "utf8", timeout: 15_000, windowsHide: true,
});
assert.equal(failure.status, 1, failure.stderr);
assert.match(failure.stderr, /ELECTRON_RUN_AS_NODE is set, run via:/);
assert.doesNotMatch(failure.stderr, /TypeError/);

// Compile the real watchdog; replace only its analytics/route dependencies so
// synthetic stalls never send events to a backend or analytics service.
const source = (await readFile(join(root, "packages/core/diagnostics/freeze-watchdog.ts"), "utf8"))
  .replace('import { captureEvent } from "../analytics";',
    "const captureEvent = (event, properties) => window.__events.push({ event, properties });")
  .replace('import { getDiagnosticRoute } from "./diagnostic-context";',
    'const getDiagnosticRoute = () => "/:slug/inbox";');
const compiled = ts.transpileModule(source, {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ES2022 },
}).outputText;
const server = createServer((_request, response) => {
  response.setHeader("Content-Type", "text/html");
  response.end(`<html><title>Electron diagnostic smoke</title><body>Isolated diagnostic smoke
    <script type="module">window.__events = []; ${compiled}
    installFreezeWatchdog(); window.__ready = true;</script></body></html>`);
});
await new Promise((resolve, reject) => {
  server.once("error", reject);
  server.listen(0, "127.0.0.1", resolve);
});
let application;
try {
  const url = `http://127.0.0.1:${server.address().port}`;
  application = await _electron.launch({
    args: [entry, url, `--user-data-dir=${profile}`],
    env: electronEnv.envForElectron(polluted), timeout: 30_000,
  });
  const page = await application.firstWindow();
  await page.waitForFunction(() => window.__ready);
  const windowState = await application.evaluate(({ BrowserWindow }) => {
    const window = BrowserWindow.getAllWindows()[0];
    window.show();
    return {
      visible: window.isVisible(), nodeMode: process.env.ELECTRON_RUN_AS_NODE ?? null,
      executablePath: process.execPath, electronVersion: process.versions.electron,
    };
  });
  assert.equal(windowState.visible, true);
  assert.equal(windowState.nodeMode, null);
  assert.equal(windowState.executablePath, executablePath);
  await page.evaluate(() => new Promise((done) => {
    setTimeout(() => {
      const start = performance.now();
      while (performance.now() - start < 3000) { /* Deliberate renderer stall. */ }
      done();
    }, 0);
  }));
  await page.waitForFunction(() => window.__events.length > 0);
  const events = await page.evaluate(() => window.__events);
  assert.equal(events.length, 1);
  const event = events[0];
  assert.equal(event.event, "client_unresponsive");
  assert.equal(event.properties.source, "longtask");
  assert.ok(event.properties.duration_ms >= 2900);
  assert.equal(typeof event.properties.name, "string");
  assert.ok(Array.isArray(event.properties.attribution));
  for (const attribution of event.properties.attribution) {
    assert.equal(typeof attribution.containerName, "string");
    assert.equal(typeof attribution.containerSrc, "string");
  }
  if (output) await page.screenshot({ path: output.replace(/\.json$/, "") + ".png" });
  const result = {
    recordedAt: new Date().toISOString(), entry, profile,
    polluted: { exitCode: failure.status, stderr: failure.stderr.trim() },
    clean: windowState, events,
    scope: "Real Electron + real watchdog; captureEvent intercepted locally; no backend/analytics requests.",
  };
  if (output) await writeFile(output, JSON.stringify(result, null, 2));
  console.log(JSON.stringify(result, null, 2));
} finally {
  if (application) await application.close();
  await new Promise((resolve, reject) => server.close(error => error ? reject(error) : resolve()));
}
