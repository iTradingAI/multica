import { randomUUID } from "node:crypto";
import {
  TERMINAL_MAX_SESSIONS,
  type TerminalDataEvent,
  type TerminalExitEvent,
  type TerminalSpawnRequest,
  type TerminalSpawnResult,
  type TerminalResizeInput,
  type TerminalWriteInput,
} from "../shared/terminal-types";

/**
 * Structural type over node-pty's IPty. Declared locally (not imported
 * from "node-pty") so this module compiles and its unit tests run without
 * loading the native addon — only terminal-ipc.ts, which tests never
 * touch, imports the real binding.
 */
export interface PtyProcess {
  readonly pid: number;
  write(data: string): void;
  resize(cols: number, rows: number): void;
  kill(signal?: string): void;
  onData(listener: (data: string) => void): { dispose(): void };
  onExit(listener: (event: { exitCode: number }) => void): { dispose(): void };
}

export interface PtySpawnOptions {
  file: string;
  args?: string[];
  cwd?: string;
  cols: number;
  rows: number;
  env?: Record<string, string>;
}

export type PtySpawn = (options: PtySpawnOptions) => PtyProcess;

/**
 * The renderer side of a session owner: a WebContents. Captured
 * structurally so tests can pass a plain object.
 */
export interface TerminalOwner {
  send(channel: "terminal:data" | "terminal:exit", payload: unknown): void;
  isDestroyed(): boolean;
  /** Subscribe to teardown; returns an unsubscribe function. */
  onDestroyed(listener: () => void): () => void;
}

export interface TerminalManagerDeps {
  spawnPty: PtySpawn;
  /** Resolve the default shell for the host platform. */
  resolveShell: () => { file: string; args?: string[] };
  /** Validate that cwd exists and is a directory. */
  isDirectory: (path: string) => boolean;
  /** Validate that an explicit shell override exists and is a file. */
  isExecutableFile: (path: string) => boolean;
}

interface ManagedSession {
  sessionId: string;
  process: PtyProcess;
  owner: TerminalOwner;
  disposeOwnerListener: () => void;
  disposed: boolean;
}

/**
 * Owns every live pty for the app: spawning with validated input, routing
 * output/exit events to the owning renderer, and guaranteeing no process
 * outlives its window. Pure TypeScript with injected effects — the
 * Electron/node-pty wiring lives in terminal-ipc.ts.
 */
export class TerminalManager {
  private readonly sessions = new Map<string, ManagedSession>();

  constructor(private readonly deps: TerminalManagerDeps) {}

  sessionCount(): number {
    return this.sessions.size;
  }

  createSession(
    request: TerminalSpawnRequest,
    owner: TerminalOwner,
  ): TerminalSpawnResult {
    if (this.sessions.size >= TERMINAL_MAX_SESSIONS) {
      return {
        ok: false,
        reason: "limit_reached",
        message: `at most ${TERMINAL_MAX_SESSIONS} terminal sessions can be open at once`,
      };
    }
    if (request.cwd !== undefined && !this.deps.isDirectory(request.cwd)) {
      return {
        ok: false,
        reason: "invalid_request",
        message: "cwd is not an existing directory",
      };
    }
    if (
      request.shell !== undefined &&
      !this.deps.isExecutableFile(request.shell)
    ) {
      return {
        ok: false,
        reason: "invalid_request",
        message: "shell is not an existing executable file",
      };
    }

    const command =
      request.shell !== undefined
        ? { file: request.shell }
        : this.deps.resolveShell();

    let process: PtyProcess;
    try {
      process = this.deps.spawnPty({
        file: command.file,
        ...(command.args !== undefined ? { args: command.args } : {}),
        ...(request.cwd !== undefined ? { cwd: request.cwd } : {}),
        cols: request.cols,
        rows: request.rows,
      });
    } catch (err) {
      return {
        ok: false,
        reason: "spawn_failed",
        message: err instanceof Error ? err.message : String(err),
      };
    }

    const sessionId = randomUUID();
    const session: ManagedSession = {
      sessionId,
      process,
      owner,
      disposeOwnerListener: () => {},
      disposed: false,
    };

    // If the owning window goes away (close, crash, reload), kill its
    // ptys immediately — a shell must never outlive the renderer that
    // asked for it.
    session.disposeOwnerListener = owner.onDestroyed(() => {
      this.dropSession(sessionId, 0);
    });

    process.onData((data) => {
      if (session.disposed || owner.isDestroyed()) return;
      owner.send("terminal:data", {
        sessionId,
        data,
      } satisfies TerminalDataEvent);
    });
    process.onExit((event) => {
      if (session.disposed) return;
      owner.send("terminal:exit", {
        sessionId,
        exitCode: event.exitCode,
      } satisfies TerminalExitEvent);
      this.dropSession(sessionId, event.exitCode, { alreadyExited: true });
    });

    this.sessions.set(sessionId, session);
    return { ok: true, sessionId };
  }

  write(input: TerminalWriteInput): boolean {
    const session = this.sessions.get(input.sessionId);
    if (!session || session.disposed) return false;
    session.process.write(input.data);
    return true;
  }

  resize(input: TerminalResizeInput): boolean {
    const session = this.sessions.get(input.sessionId);
    if (!session || session.disposed) return false;
    session.process.resize(input.cols, input.rows);
    return true;
  }

  kill(sessionId: string): boolean {
    const session = this.sessions.get(sessionId);
    if (!session || session.disposed) return false;
    this.dropSession(sessionId, 0);
    return true;
  }

  /** Kill every session regardless of owner. Used at app shutdown. */
  disposeAll(): void {
    for (const sessionId of [...this.sessions.keys()]) {
      this.dropSession(sessionId, 0);
    }
  }

  private dropSession(
    sessionId: string,
    _exitCode: number,
    opts?: { alreadyExited?: boolean },
  ): void {
    const session = this.sessions.get(sessionId);
    if (!session || session.disposed) return;
    session.disposed = true;
    session.disposeOwnerListener();
    if (!opts?.alreadyExited) {
      // pty.kill on an already-exited process is harmless, but skip it
      // when the exit callback drove this drop.
      try {
        session.process.kill();
      } catch {
        // The process may have reaped itself between the exit event and
        // this call; nothing to recover.
      }
    }
    this.sessions.delete(sessionId);
  }
}
