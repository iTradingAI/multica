#!/usr/bin/env node
// Shared GUI launch boundary for both dev and preview.
import { spawnSync } from "node:child_process";
import { envWithLocalBins } from "./package.mjs";
import electronEnv from "./electron-env.cjs";

// Include both desktop-local and repo-root bins for isolated/hoisted installs.
const result = spawnSync("electron-vite", process.argv.slice(2), {
  stdio: "inherit",
  env: electronEnv.envForElectron(envWithLocalBins()),
  shell: process.platform === "win32",
});
if (result.error) console.error(`[electron-vite] ${result.error.message}`);
process.exit(result.status ?? 1);
