import type {
  TerminalExitInfo,
  TerminalOpenOptions,
  TerminalOpenResult,
  TerminalSessionHandle,
  TerminalSessionSource,
  TerminalTarget,
} from "./session-source";

/**
 * Client side of the realtime-hub terminal relay (MAX-51 M2/M3). Frames are
 * `{type, payload}` envelopes on the same authenticated WebSocket the app
 * already uses; the transport is injected so this class stays unit-testable.
 */
export interface RealtimeTransport {
  subscribe(type: string, handler: (payload: unknown) => void): () => void;
  send(message: { type: string; payload: unknown }): void;
}

const OPEN_TIMEOUT_MS = 10_000;
const SUBSCRIBE_TIMEOUT_MS = 10_000;

// ---------------------------------------------------------------------------
// Base64 framing. pty bytes travel as base64 (protocol.TerminalInputPayload's
// []byte field encodes to a base64 JSON string over the wire in Go). The
// helpers are UTF-8 safe: TextEncoder/TextDecoder bridge JS strings to bytes.
// ---------------------------------------------------------------------------

export function encodeBase64(text: string): string {
  const bytes = new TextEncoder().encode(text);
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

export function decodeBase64(encoded: string): string {
  const binary = atob(encoded);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) bytes[i] = binary.charCodeAt(i);
  return new TextDecoder().decode(bytes);
}

interface PendingOpen {
  resolve: (result: TerminalOpenResult) => void;
  reqId: string;
  runtimeId: string;
  timer: ReturnType<typeof setTimeout>;
}

interface LiveSession {
  dataHandlers: Set<(data: string) => void>;
  exitHandlers: Set<(exit: TerminalExitInfo) => void>;
}

interface SubscribeWaiter {
  resolve: (error: string | null) => void;
  timer: ReturnType<typeof setTimeout>;
}

function randomReqId(): string {
  return `req-${Math.random().toString(36).slice(2)}-${Date.now().toString(36)}`;
}

export class RemoteSessionSource implements TerminalSessionSource {
  readonly kind = "remote" as const;
  private subscribed = false;
  private pendingOpens = new Map<string, PendingOpen>();
  private subscribeWaiters = new Map<string, SubscribeWaiter>();
  private sessions = new Map<string, LiveSession>();

  constructor(private transport: RealtimeTransport) {}

  private ensureSubscribed(): void {
    if (this.subscribed) return;
    this.subscribed = true;
    this.transport.subscribe("subscribe_ack", (payload) => {
      const p = payload as { scope?: string; id?: string };
      if (p?.scope !== "terminal" || !p.id) return;
      const waiter = this.subscribeWaiters.get(p.id);
      if (!waiter) return;
      clearTimeout(waiter.timer);
      this.subscribeWaiters.delete(p.id);
      waiter.resolve(null);
    });
    this.transport.subscribe("subscribe_error", (payload) => {
      const p = payload as { scope?: string; id?: string; error?: string };
      if (p?.scope !== "terminal" || !p.id) return;
      const waiter = this.subscribeWaiters.get(p.id);
      if (waiter) {
        clearTimeout(waiter.timer);
        this.subscribeWaiters.delete(p.id);
        waiter.resolve(p.error ?? "lookup_failed");
      }
      // A denied subscription also fails any open in flight for this runtime.
      this.failOpensForRuntime(p.id, p.error ?? "lookup_failed");
    });
    this.transport.subscribe("terminal.open_result", (payload) => {
      const p = payload as {
        req_id?: string;
        session_id?: string;
        error?: string;
      };
      if (!p?.req_id) return;
      const pending = this.pendingOpens.get(p.req_id);
      if (!pending) return;
      clearTimeout(pending.timer);
      this.pendingOpens.delete(p.req_id);
      if (p.session_id && !p.error) {
        pending.resolve({ ok: true, handle: this.registerSession(p.session_id) });
      } else {
        pending.resolve({ ok: false, error: p.error ?? "spawn_failed" });
      }
    });
    this.transport.subscribe("terminal.data", (payload) => {
      const p = payload as { session_id?: string; data?: string };
      const session = this.sessions.get(p?.session_id ?? "");
      if (!session || typeof p.data !== "string") return;
      const text = decodeBase64(p.data);
      for (const handler of session.dataHandlers) handler(text);
    });
    this.transport.subscribe("terminal.exit", (payload) => {
      const p = payload as {
        session_id?: string;
        code?: number;
        reason?: string;
      };
      if (!p?.session_id) return;
      this.finishSession(p.session_id, { code: p.code, reason: p.reason });
    });
    this.transport.subscribe("terminal.error", (payload) => {
      const p = payload as {
        session_id?: string;
        req_id?: string;
        error?: string;
      };
      const error = p?.error ?? "unknown";
      if (p?.req_id) {
        const pending = this.pendingOpens.get(p.req_id);
        if (pending) {
          clearTimeout(pending.timer);
          this.pendingOpens.delete(p.req_id);
          pending.resolve({ ok: false, error });
        }
      }
      if (p?.session_id) {
        this.finishSession(p.session_id, { reason: error });
      }
    });
  }

  private registerSession(sessionId: string): TerminalSessionHandle {
    const live: LiveSession = {
      dataHandlers: new Set(),
      exitHandlers: new Set(),
    };
    this.sessions.set(sessionId, live);
    return {
      session: sessionId,
      onData: (callback) => {
        live.dataHandlers.add(callback);
        return () => {
          live.dataHandlers.delete(callback);
        };
      },
      onExit: (callback) => {
        live.exitHandlers.add(callback);
        return () => {
          live.exitHandlers.delete(callback);
        };
      },
    };
  }

  private finishSession(sessionId: string, exit: TerminalExitInfo): void {
    const live = this.sessions.get(sessionId);
    if (!live) return;
    this.sessions.delete(sessionId);
    for (const handler of live.exitHandlers) handler(exit);
  }

  private failOpensForRuntime(runtimeId: string, error: string): void {
    for (const pending of this.pendingOpens.values()) {
      if (pending.runtimeId === runtimeId) {
        clearTimeout(pending.timer);
        pending.resolve({ ok: false, error });
        this.pendingOpens.delete(pending.reqId);
      }
    }
  }

  /**
   * Release the runtime's exclusive terminal scope: fail anything still
   * pending for it and unsubscribe, letting the hub kill its sessions and
   * free the scope for other clients. Idempotent — the hub treats a
   * redundant unsubscribe as a no-op, and a later open() re-subscribes.
   */
  release(target: TerminalTarget): void {
    const waiter = this.subscribeWaiters.get(target);
    if (waiter) {
      clearTimeout(waiter.timer);
      this.subscribeWaiters.delete(target);
      waiter.resolve("released");
    }
    this.failOpensForRuntime(target, "released");
    this.transport.send({
      type: "unsubscribe",
      payload: { scope: "terminal", id: target },
    });
  }

  async open(
    target: TerminalTarget,
    options: TerminalOpenOptions,
  ): Promise<TerminalOpenResult> {
    this.ensureSubscribed();

    const subscribeError = await this.subscribe(target);
    if (subscribeError) return { ok: false, error: subscribeError };

    // The shell stays unset unless the caller explicitly chose one: the LOCAL
    // default shell (Git Bash on Windows) is meaningless on the target
    // machine. Unset lets the daemon pick its platform default (bash/zsh),
    // which is what a remote tab wants.
    const shell = options.shell;

    const reqId = randomReqId();
    const openResult = await new Promise<TerminalOpenResult>((resolve) => {
      const timer = setTimeout(() => {
        this.pendingOpens.delete(reqId);
        resolve({ ok: false, error: "timeout" });
      }, OPEN_TIMEOUT_MS);
      this.pendingOpens.set(reqId, { resolve, reqId, runtimeId: target, timer });
      this.transport.send({
        type: "terminal.open",
        payload: {
          req_id: reqId,
          shell,
          cwd: options.cwd,
          cols: options.cols,
          rows: options.rows,
        },
      });
    });
    return openResult;
  }

  /** Subscribe to the terminal scope for a runtime; resolves with an error reason or null. */
  private subscribe(runtimeId: string): Promise<string | null> {
    return new Promise((resolve) => {
      const timer = setTimeout(() => {
        this.subscribeWaiters.delete(runtimeId);
        resolve("timeout");
      }, SUBSCRIBE_TIMEOUT_MS);
      this.subscribeWaiters.set(runtimeId, { resolve, timer });
      this.transport.send({
        type: "subscribe",
        payload: { scope: "terminal", id: runtimeId },
      });
    });
  }

  write(session: string, data: string): void {
    this.transport.send({
      type: "terminal.input",
      payload: { session_id: session, data: encodeBase64(data) },
    });
  }

  resize(session: string, cols: number, rows: number): void {
    this.transport.send({
      type: "terminal.resize",
      payload: { session_id: session, cols, rows },
    });
  }

  kill(session: string): void {
    this.transport.send({
      type: "terminal.kill",
      payload: { session_id: session },
    });
    this.sessions.delete(session);
  }
}
