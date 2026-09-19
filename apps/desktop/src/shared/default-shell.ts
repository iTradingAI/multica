import { statSync } from "node:fs";

/**
 * Default shell selection shared by the main-process pty host (local
 * sessions) and the renderer's remote `terminal.open` payload (remote
 * sessions). Kept as a pure function (platform + env + existence probe in,
 * command out) so the matrix is unit-testable without Electron or the real
 * environment.
 *
 * Priority on Windows: Git Bash when installed in any common location,
 * then COMSPEC (points at cmd.exe), then PowerShell when COMSPEC is unset.
 * Priority on unix-likes: $SHELL (set by every sane login manager), then the
 * platform fallback (zsh on macOS, bash elsewhere) — unchanged by this
 * helper.
 */

/** The resolved default shell command main hands to node-pty / remote opens. */
export interface DefaultShell {
  file: string;
  args?: string[];
}

/** Common Git Bash install locations, most reliable first. */
export function gitBashCandidatePaths(env: NodeJS.ProcessEnv): string[] {
  const programFiles = env.ProgramFiles || "C:\\Program Files";
  const programFilesX86 = env["ProgramFiles(x86)"] || "C:\\Program Files (x86)";
  const localAppData = env.LOCALAPPDATA;
  const candidates = [
    `${programFiles}\\Git\\bin\\bash.exe`,
    `${programFilesX86}\\Git\\bin\\bash.exe`,
  ];
  if (localAppData) {
    candidates.push(`${localAppData}\\Programs\\Git\\bin\\bash.exe`);
  }
  return candidates;
}

/**
 * First candidate that exists, or null when Git Bash is not installed. The
 * existence probe is injected so tests (and non-Node callers) can stub it.
 */
export function resolveGitBash(
  candidates: string[],
  exists: (path: string) => boolean,
): string | null {
  for (const candidate of candidates) {
    if (candidate && exists(candidate)) return candidate;
  }
  return null;
}

function defaultExists(path: string): boolean {
  try {
    return statSync(path).isFile();
  } catch {
    return false;
  }
}

export function resolveDefaultShell(
  platform: NodeJS.Platform = process.platform,
  env: NodeJS.ProcessEnv = process.env,
  exists: (path: string) => boolean = defaultExists,
): DefaultShell {
  if (platform === "win32") {
    const gitBash = resolveGitBash(gitBashCandidatePaths(env), exists);
    if (gitBash) return { file: gitBash };
    return { file: env.COMSPEC || "powershell.exe" };
  }
  if (env.SHELL) {
    return { file: env.SHELL };
  }
  return { file: platform === "darwin" ? "/bin/zsh" : "/bin/bash" };
}
