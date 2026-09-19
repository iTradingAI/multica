/**
 * Session-source abstraction for the floating terminal panel (MAX-51 M4).
 * The panel no longer hardcodes the local `terminalAPI` bridge: a source
 * opens a shell session on a target ("local" or a runtime id) and exposes
 * uniform write/resize/kill plus per-session data/exit streams.
 */

/** A session target: "local" for the machine the app runs on, otherwise a runtime id. */
export type TerminalTarget = string;

export interface TerminalOpenOptions {
  cols: number;
  rows: number;
  shell?: string;
  cwd?: string;
}

/** Why a session ended. `reason` carries server-synthesized exits such as "daemon_offline". */
export interface TerminalExitInfo {
  code?: number;
  reason?: string;
}

export interface TerminalSessionHandle {
  session: string;
  /** Register a listener for decoded pty output. Returns an unsubscribe. */
  onData(callback: (data: string) => void): () => void;
  /** Register a listener for session end. Returns an unsubscribe. */
  onExit(callback: (exit: TerminalExitInfo) => void): () => void;
}

export type TerminalOpenResult =
  | { ok: true; handle: TerminalSessionHandle }
  | { ok: false; error: string };

export interface TerminalSessionSource {
  readonly kind: "local" | "remote";
  open(target: TerminalTarget, options: TerminalOpenOptions): Promise<TerminalOpenResult>;
  write(session: string, data: string): void;
  resize(session: string, cols: number, rows: number): void;
  kill(session: string): void;
}

/**
 * Map a subscribe_error reason (or terminal.open_result error) to an i18n
 * key under settings.desktop.terminal. Unknown reasons fall back to the
 * generic error key.
 */
export function terminalErrorKey(error: string): string {
  switch (error) {
    case "in use":
      return "err_in_use";
    case "forbidden":
      return "err_forbidden";
    case "capability_missing":
      return "err_unsupported";
    case "runtime_offline":
      return "err_offline";
    default:
      return "err_generic";
  }
}
