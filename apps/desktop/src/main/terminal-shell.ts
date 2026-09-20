import { statSync } from "node:fs";
import type { PtySpawnOptions } from "./terminal-manager";

/**
 * Default shell selection for the local pty host. The pure logic lives in
 * shared/default-shell.ts so the renderer can resolve the same default for
 * remote `terminal.open` payloads; this module adds the real filesystem
 * probe and re-exports for main-process callers.
 */
export {
  gitBashCandidatePaths,
  resolveDefaultShell,
  resolveGitBash,
} from "../shared/default-shell";
import type { DefaultShell } from "../shared/default-shell";
import { resolveDefaultShell as resolveDefaultShellPure } from "../shared/default-shell";

/** Local default shell, resolved against the real filesystem. */
export function resolveLocalDefaultShell(): DefaultShell {
  return resolveDefaultShellPure(
    process.platform,
    process.env,
    fsIsExecutableFile,
  );
}

export function fsIsDirectory(path: string): boolean {
  try {
    return statSync(path).isDirectory();
  } catch {
    return false;
  }
}

export function fsIsExecutableFile(path: string): boolean {
  try {
    return statSync(path).isFile();
  } catch {
    return false;
  }
}

/**
 * Environment handed to every spawned shell. process.env carries undefined
 * values for unset keys, which node-pty rejects — strip them and pin a
 * color-capable TERM so prompt/tooling output matches the xterm renderer.
 */
export function shellEnv(
  env: NodeJS.ProcessEnv = process.env,
): Record<string, string> {
  const clean: Record<string, string> = {};
  for (const [key, value] of Object.entries(env)) {
    if (value !== undefined) clean[key] = value;
  }
  clean.TERM = "xterm-256color";
  return clean;
}

export type { PtySpawnOptions };
