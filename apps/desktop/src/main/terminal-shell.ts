import { statSync } from "node:fs";
import type { PtySpawnOptions } from "./terminal-manager";

/**
 * Default shell selection for the floating terminal. Kept as a pure
 * function (platform + env in, command out) so the matrix is unit-testable
 * without touching Electron or the real environment.
 *
 * Priority: $SHELL (set by every sane login manager on unix-likes),
 * platform fallback (zsh on macOS, bash elsewhere), then COMSPEC on
 * Windows (points at cmd.exe; fall back to PowerShell when unset).
 */
export function resolveDefaultShell(
  platform: NodeJS.Platform = process.platform,
  env: NodeJS.ProcessEnv = process.env,
): { file: string; args?: string[] } {
  if (platform === "win32") {
    return { file: env.COMSPEC || "powershell.exe" };
  }
  if (env.SHELL) {
    return { file: env.SHELL };
  }
  return { file: platform === "darwin" ? "/bin/zsh" : "/bin/bash" };
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
