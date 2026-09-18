// IPC contract between the renderer terminal panel and the main-process
// pty host. Every payload crossing this bridge is parsed with the
// validators below — renderer input is untrusted at process boundaries,
// the same policy daemon-types.ts applies.

/** Hard cap on live sessions per app instance. */
export const TERMINAL_MAX_SESSIONS = 5;

export const TERMINAL_MIN_COLS = 2;
export const TERMINAL_MAX_COLS = 500;
export const TERMINAL_MIN_ROWS = 2;
export const TERMINAL_MAX_ROWS = 300;

// A keystroke is a few bytes; a large paste can be hundreds of KB. Cap a
// single write chunk so a runaway renderer cannot pin the main process
// shuttling unbounded data to the pty.
export const TERMINAL_MAX_WRITE_BYTES = 256 * 1024;

export interface TerminalSpawnRequest {
  cols: number;
  rows: number;
  /** Working directory for the shell. Must be an existing directory. */
  cwd?: string;
  /**
   * Absolute path of the executable to run instead of the default shell
   * (used by quick-launch presets such as the bundled CLI). Must be an
   * existing file.
   */
  shell?: string;
}

export type TerminalSpawnResult =
  | { ok: true; sessionId: string }
  | {
      ok: false;
      reason: "invalid_request" | "limit_reached" | "spawn_failed";
      message?: string;
    };

export interface TerminalWriteInput {
  sessionId: string;
  data: string;
}

export interface TerminalResizeInput {
  sessionId: string;
  cols: number;
  rows: number;
}

export interface TerminalKillInput {
  sessionId: string;
}

/** Main → renderer: a chunk of pty output. */
export interface TerminalDataEvent {
  sessionId: string;
  data: string;
}

/** Main → renderer: the pty exited; drop the session UI. */
export interface TerminalExitEvent {
  sessionId: string;
  exitCode: number;
}

// Session ids are minted in the main process with crypto.randomUUID().
// Pinning the shape here lets every later message reject fabricated or
// truncated ids before they reach the session map.
const SESSION_ID_PATTERN =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

export function isTerminalSessionId(value: unknown): value is string {
  return typeof value === "string" && SESSION_ID_PATTERN.test(value);
}

function parseBoundedInt(
  value: unknown,
  min: number,
  max: number,
): number | null {
  if (typeof value !== "number") return null;
  if (!Number.isInteger(value)) return null;
  if (value < min || value > max) return null;
  return value;
}

// Optional string field: absent, undefined, or empty means "not provided";
// anything else must be a non-empty string within a sane length bound so a
// hostile renderer cannot smuggle oversized paths through validation.
// Returns null for a present-but-invalid value (the caller rejects).
function parseOptionalString(
  value: unknown,
  maxLength: number,
): string | undefined | null {
  if (value === undefined || value === null || value === "") return undefined;
  if (typeof value !== "string" || value.length > maxLength) return null;
  return value;
}

export function parseTerminalSpawnRequest(
  value: unknown,
): TerminalSpawnRequest | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const obj = value as Record<string, unknown>;
  const cols = parseBoundedInt(obj.cols, TERMINAL_MIN_COLS, TERMINAL_MAX_COLS);
  if (cols === null) return null;
  const rows = parseBoundedInt(obj.rows, TERMINAL_MIN_ROWS, TERMINAL_MAX_ROWS);
  if (rows === null) return null;
  // 4096 comfortably covers filesystem paths on every supported platform.
  const cwd = parseOptionalString(obj.cwd, 4096);
  const shell = parseOptionalString(obj.shell, 4096);
  if (cwd === null || shell === null) return null;
  if (cwd !== undefined && !cwd.startsWith("/")) return null;
  if (shell !== undefined && !shell.startsWith("/")) return null;
  return { cols, rows, ...(cwd !== undefined ? { cwd } : {}), ...(shell !== undefined ? { shell } : {}) };
}

export function parseTerminalWriteInput(
  value: unknown,
): TerminalWriteInput | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const obj = value as Record<string, unknown>;
  if (!isTerminalSessionId(obj.sessionId)) return null;
  if (typeof obj.data !== "string") return null;
  // Byte bound, not code-point bound: the pty consumes bytes.
  if (Buffer.byteLength(obj.data, "utf8") > TERMINAL_MAX_WRITE_BYTES) {
    return null;
  }
  return { sessionId: obj.sessionId, data: obj.data };
}

export function parseTerminalResizeInput(
  value: unknown,
): TerminalResizeInput | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const obj = value as Record<string, unknown>;
  if (!isTerminalSessionId(obj.sessionId)) return null;
  const cols = parseBoundedInt(obj.cols, TERMINAL_MIN_COLS, TERMINAL_MAX_COLS);
  if (cols === null) return null;
  const rows = parseBoundedInt(obj.rows, TERMINAL_MIN_ROWS, TERMINAL_MAX_ROWS);
  if (rows === null) return null;
  return { sessionId: obj.sessionId, cols, rows };
}

export function parseTerminalKillInput(
  value: unknown,
): TerminalKillInput | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const obj = value as Record<string, unknown>;
  if (!isTerminalSessionId(obj.sessionId)) return null;
  return { sessionId: obj.sessionId };
}
