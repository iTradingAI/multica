import type {
  TerminalExitInfo,
  TerminalOpenOptions,
  TerminalOpenResult,
  TerminalSessionHandle,
  TerminalSessionSource,
  TerminalTarget,
} from "./session-source";

type Bridge = {
  spawn: (request: {
    cols: number;
    rows: number;
    shell?: string;
    cwd?: string;
  }) => Promise<{ ok: true; sessionId: string } | { ok: false; reason: string }>;
  write: (sessionId: string, data: string) => void;
  resize: (sessionId: string, cols: number, rows: number) => void;
  kill: (sessionId: string) => void;
  onData: (
    callback: (event: { sessionId: string; data: string }) => void,
  ) => () => void;
  onExit: (
    callback: (event: { sessionId: string; exitCode: number }) => void,
  ) => () => void;
};

/**
 * Session source for the machine the app runs on: a thin adapter over the
 * preload `terminalAPI` bridge. One global data/exit subscription is taken
 * lazily and demultiplexed per session id.
 */
export class LocalSessionSource implements TerminalSessionSource {
  readonly kind = "local" as const;
  private dataHandlers = new Map<string, Set<(data: string) => void>>();
  private exitHandlers = new Map<string, Set<(exit: TerminalExitInfo) => void>>();
  private subscribed = false;

  constructor(private bridge: Bridge) {}

  private ensureSubscribed(): void {
    if (this.subscribed) return;
    this.subscribed = true;
    this.bridge.onData((event) => {
      const handlers = this.dataHandlers.get(event.sessionId);
      if (!handlers) return;
      for (const handler of handlers) handler(event.data);
    });
    this.bridge.onExit((event) => {
      const handlers = this.exitHandlers.get(event.sessionId);
      if (!handlers) return;
      for (const handler of handlers) handler({ code: event.exitCode });
    });
  }

  async open(
    _target: TerminalTarget,
    options: TerminalOpenOptions,
  ): Promise<TerminalOpenResult> {
    const result = await this.bridge.spawn(options);
    if (!result.ok) return { ok: false, error: result.reason };
    this.ensureSubscribed();
    const session = result.sessionId;
    const handle: TerminalSessionHandle = {
      session,
      onData: (callback) => {
        let handlers = this.dataHandlers.get(session);
        if (!handlers) {
          handlers = new Set();
          this.dataHandlers.set(session, handlers);
        }
        handlers.add(callback);
        return () => {
          handlers?.delete(callback);
        };
      },
      onExit: (callback) => {
        let handlers = this.exitHandlers.get(session);
        if (!handlers) {
          handlers = new Set();
          this.exitHandlers.set(session, handlers);
        }
        handlers.add(callback);
        return () => {
          handlers?.delete(callback);
        };
      },
    };
    return { ok: true, handle };
  }

  write(session: string, data: string): void {
    this.bridge.write(session, data);
  }

  resize(session: string, cols: number, rows: number): void {
    this.bridge.resize(session, cols, rows);
  }

  kill(session: string): void {
    this.bridge.kill(session);
    this.dataHandlers.delete(session);
    this.exitHandlers.delete(session);
  }
}
