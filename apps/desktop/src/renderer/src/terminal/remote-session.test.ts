// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import {
  RemoteSessionSource,
  decodeBase64,
  encodeBase64,
  type RealtimeTransport,
} from "./remote-session";

// ---------------------------------------------------------------------------
// The transport is a scripted fake: it records every outbound frame and lets
// tests push inbound payloads, exactly the two directions the source uses.
// ---------------------------------------------------------------------------

interface SentFrame {
  type: string;
  payload: Record<string, unknown>;
}

function makeTransport() {
  const sent: SentFrame[] = [];
  const handlers = new Map<string, Set<(payload: unknown) => void>>();
  const transport: RealtimeTransport = {
    subscribe: (type, handler) => {
      let set = handlers.get(type);
      if (!set) {
        set = new Set();
        handlers.set(type, set);
      }
      set.add(handler);
      return () => set.delete(handler);
    },
    send: (message) => {
      sent.push(message as SentFrame);
    },
  };
  const frames = sent;
  return {
    transport,
    frames,
    emit: (type: string, payload: Record<string, unknown>) => {
      for (const handler of handlers.get(type) ?? []) handler(payload);
    },
  };
}

describe("base64 frame encoding", () => {
  it("round-trips ASCII and multi-byte UTF-8", () => {
    for (const text of ["ls\r\n", "你好 world ✓", "\u0000\u001b[1m"]) {
      expect(decodeBase64(encodeBase64(text))).toBe(text);
    }
  });

  it("matches the Go-side base64 std encoding for a known vector", () => {
    expect(encodeBase64("hello")).toBe(btoa("hello"));
  });
});

describe("RemoteSessionSource", () => {
  it("subscribes to the terminal scope and correlates open_result by req_id", async () => {
    const { transport, frames, emit } = makeTransport();
    const source = new RemoteSessionSource(transport);
    const openPromise = source.open("runtime-1", { cols: 80, rows: 24 });

    // First frame: the scope subscription.
    expect(frames[0]).toEqual({
      type: "subscribe",
      payload: { scope: "terminal", id: "runtime-1" },
    });
    emit("subscribe_ack", { scope: "terminal", id: "runtime-1" });

    // Second frame: terminal.open, with a req_id we echo back.
    await vi.waitFor(() => expect(frames).toHaveLength(2));
    const open = frames[1]!;
    expect(open.type).toBe("terminal.open");
    const reqId = open.payload.req_id as string;
    expect(reqId).toBeTruthy();
    expect(open.payload).toMatchObject({ cols: 80, rows: 24 });

    const resultPromise = Promise.resolve(openPromise);
    emit("terminal.open_result", {
      req_id: "wrong-req",
      session_id: "decoy",
    });
    emit("terminal.open_result", { req_id: reqId, session_id: "sess-1" });

    const result = await resultPromise;
    expect(result.ok).toBe(true);
    if (!result.ok) throw new Error("unreachable");
    expect(result.handle.session).toBe("sess-1");
  });

  it("decodes base64 terminal.data for the right session only", async () => {
    const { transport, frames, emit } = makeTransport();
    const source = new RemoteSessionSource(transport);
    const openPromise = source.open("runtime-1", { cols: 80, rows: 24 });
    emit("subscribe_ack", { scope: "terminal", id: "runtime-1" });
    await vi.waitFor(() => expect(frames).toHaveLength(2));
    emit("terminal.open_result", {
      req_id: frames[1]!.payload.req_id,
      session_id: "sess-1",
    });
    const result = await openPromise;
    if (!result.ok) throw new Error("open failed");
    expect(result.ok).toBe(true);

    const received: string[] = [];
    result.handle.onData((data) => received.push(data));

    emit("terminal.data", {
      session_id: "other",
      data: encodeBase64("ignored"),
    });
    emit("terminal.data", { session_id: "sess-1", data: encodeBase64("hi→") });
    expect(received).toEqual(["hi→"]);
  });

  it("encodes input to base64 and routes write/resize/kill", async () => {
    const { transport, frames, emit } = makeTransport();
    const source = new RemoteSessionSource(transport);
    const openPromise = source.open("runtime-1", { cols: 80, rows: 24 });
    emit("subscribe_ack", { scope: "terminal", id: "runtime-1" });
    await vi.waitFor(() => expect(frames).toHaveLength(2));
    emit("terminal.open_result", {
      req_id: frames[1]!.payload.req_id,
      session_id: "sess-1",
    });
    const result = await openPromise;
    if (!result.ok) throw new Error("open failed");

    source.write("sess-1", "ls\r");
    source.resize("sess-1", 120, 40);
    source.kill("sess-1");

    expect(frames.slice(2)).toEqual([
      {
        type: "terminal.input",
        payload: {
          session_id: "sess-1",
          runtime_id: "runtime-1",
          data: encodeBase64("ls\r"),
        },
      },
      {
        type: "terminal.resize",
        payload: { session_id: "sess-1", runtime_id: "runtime-1", cols: 120, rows: 40 },
      },
      {
        type: "terminal.kill",
        payload: { session_id: "sess-1", runtime_id: "runtime-1" },
      },
    ]);
  });

  it("routes every session frame by its runtime, not the client's sole scope", async () => {
    // Regression (MAX-129): with two machine tabs open the client holds two
    // terminal scopes, and the hub can no longer infer the target runtime —
    // frames must carry runtime_id or the hub answers not_subscribed.
    const { transport, frames, emit } = makeTransport();
    const source = new RemoteSessionSource(transport);

    for (const runtime of ["runtime-1", "runtime-2"]) {
      const open = source.open(runtime, { cols: 80, rows: 24 });
      emit("subscribe_ack", { scope: "terminal", id: runtime });
      await vi.waitFor(() => {
        const frame = frames.find(
          (f) => f.type === "terminal.open" && f.payload.runtime_id === runtime,
        );
        expect(frame).toBeTruthy();
        expect(frame!.payload).toMatchObject({ req_id: expect.any(String) });
      });
      const frame = frames.find(
        (f) => f.type === "terminal.open" && f.payload.runtime_id === runtime,
      )!;
      emit("terminal.open_result", {
        req_id: frame.payload.req_id,
        session_id: `sess-${runtime}`,
      });
      expect((await open).ok).toBe(true);
    }

    source.write("sess-runtime-1", "whoami\r");
    source.write("sess-runtime-2", "uptime\r");
    source.resize("sess-runtime-2", 100, 30);
    source.kill("sess-runtime-1");

    expect(
      frames.filter((f) => f.type === "terminal.input").map((f) => f.payload),
    ).toEqual([
      { session_id: "sess-runtime-1", runtime_id: "runtime-1", data: encodeBase64("whoami\r") },
      { session_id: "sess-runtime-2", runtime_id: "runtime-2", data: encodeBase64("uptime\r") },
    ]);
    expect(frames.find((f) => f.type === "terminal.resize")!.payload).toEqual({
      session_id: "sess-runtime-2",
      runtime_id: "runtime-2",
      cols: 100,
      rows: 30,
    });
    expect(frames.find((f) => f.type === "terminal.kill")!.payload).toEqual({
      session_id: "sess-runtime-1",
      runtime_id: "runtime-1",
    });
  });

  it("resolves open with a subscribe_error reason", async () => {
    const { transport, frames, emit } = makeTransport();
    const source = new RemoteSessionSource(transport);
    const openPromise = source.open("runtime-1", { cols: 80, rows: 24 });
    emit("subscribe_error", {
      scope: "terminal",
      id: "runtime-1",
      error: "in use",
    });

    const result = await openPromise;
    expect(result).toEqual({ ok: false, error: "in use" });
    // The terminal.open frame is never sent.
    expect(frames).toHaveLength(1);
  });

  it("resolves open with the open_result error", async () => {
    const { transport, frames, emit } = makeTransport();
    const source = new RemoteSessionSource(transport);
    const openPromise = source.open("runtime-1", { cols: 80, rows: 24 });
    emit("subscribe_ack", { scope: "terminal", id: "runtime-1" });
    await vi.waitFor(() => expect(frames).toHaveLength(2));
    emit("terminal.open_result", {
      req_id: frames[1]!.payload.req_id,
      error: "spawn_failed",
    });
    expect(await openPromise).toEqual({ ok: false, error: "spawn_failed" });
  });

  it("surfaces daemon_offline as a synthesized exit", async () => {
    const { transport, frames, emit } = makeTransport();
    const source = new RemoteSessionSource(transport);
    const openPromise = source.open("runtime-1", { cols: 80, rows: 24 });
    emit("subscribe_ack", { scope: "terminal", id: "runtime-1" });
    await vi.waitFor(() => expect(frames).toHaveLength(2));
    emit("terminal.open_result", {
      req_id: frames[1]!.payload.req_id,
      session_id: "sess-1",
    });
    const result = await openPromise;
    if (!result.ok) throw new Error("open failed");

    const exits: Array<{ code?: number; reason?: string }> = [];
    result.handle.onExit((exit) => exits.push(exit));

    emit("terminal.exit", {
      session_id: "sess-1",
      reason: "daemon_offline",
    });
    expect(exits).toEqual([{ code: undefined, reason: "daemon_offline" }]);
  });

  it("leaves the remote shell unset so the daemon picks its platform default", async () => {
    const { transport, frames, emit } = makeTransport();
    const source = new RemoteSessionSource(transport);
    const openPromise = source.open("runtime-1", { cols: 80, rows: 24 });
    emit("subscribe_ack", { scope: "terminal", id: "runtime-1" });
    await vi.waitFor(() => expect(frames).toHaveLength(2));
    emit("terminal.open_result", {
      req_id: frames[1]!.payload.req_id,
      session_id: "sess-1",
    });
    await openPromise;
    // The LOCAL default shell (e.g. Git Bash on Windows) must not leak into a
    // remote open: that path does not exist on the target machine.
    expect(frames[1]!.payload.shell).toBeUndefined();
  });

  it("passes an explicitly chosen shell through to the open payload", async () => {
    const { transport, frames, emit } = makeTransport();
    const source = new RemoteSessionSource(transport);
    const openPromise = source.open("runtime-1", {
      cols: 80,
      rows: 24,
      shell: "/usr/bin/fish",
    });
    emit("subscribe_ack", { scope: "terminal", id: "runtime-1" });
    await vi.waitFor(() => expect(frames).toHaveLength(2));
    emit("terminal.open_result", {
      req_id: frames[1]!.payload.req_id,
      session_id: "sess-1",
    });
    await openPromise;
    expect(frames[1]!.payload.shell).toBe("/usr/bin/fish");
  });

  it("resolves open with a timeout when the relay never answers", async () => {
    vi.useFakeTimers();
    try {
      const { transport } = makeTransport();
      const source = new RemoteSessionSource(transport);
      const openPromise = source.open("runtime-1", { cols: 80, rows: 24 });
      vi.advanceTimersByTime(10_100);
      expect(await openPromise).toEqual({ ok: false, error: "timeout" });
    } finally {
      vi.useRealTimers();
    }
  });

  describe("release", () => {
    it("unsubscribes the terminal scope once no session remains", async () => {
      const { transport, frames, emit } = makeTransport();
      const source = new RemoteSessionSource(transport);
      const openPromise = source.open("runtime-1", { cols: 80, rows: 24 });
      emit("subscribe_ack", { scope: "terminal", id: "runtime-1" });
      await vi.waitFor(() => expect(frames).toHaveLength(2));
      emit("terminal.open_result", {
        req_id: frames[1]!.payload.req_id,
        session_id: "sess-1",
      });
      await openPromise;

      source.release("runtime-1");
      expect(frames[2]).toEqual({
        type: "unsubscribe",
        payload: { scope: "terminal", id: "runtime-1" },
      });
    });

    it("fails pending opens and subscribe waits for that runtime only", async () => {
      const { transport, frames } = makeTransport();
      const source = new RemoteSessionSource(transport);
      const open1 = source.open("runtime-1", { cols: 80, rows: 24 });
      const open2 = source.open("runtime-2", { cols: 80, rows: 24 });

      source.release("runtime-1");
      expect(await open1).toEqual({ ok: false, error: "released" });
      expect(frames).toContainEqual({
        type: "unsubscribe",
        payload: { scope: "terminal", id: "runtime-1" },
      });
      // runtime-2 keeps waiting; its frames are untouched.
      expect(frames).not.toContainEqual({
        type: "unsubscribe",
        payload: { scope: "terminal", id: "runtime-2" },
      });
      const open2Result = Promise.race([
        open2,
        new Promise((resolve) => setTimeout(() => resolve("still-pending"), 20)),
      ]);
      expect(await open2Result).toBe("still-pending");
    });

    it("is repeatable, and a later open re-subscribes", async () => {
      const { transport, frames, emit } = makeTransport();
      const source = new RemoteSessionSource(transport);
      const openPromise = source.open("runtime-1", { cols: 80, rows: 24 });
      emit("subscribe_ack", { scope: "terminal", id: "runtime-1" });
      await vi.waitFor(() => expect(frames).toHaveLength(2));
      emit("terminal.open_result", {
        req_id: frames[1]!.payload.req_id,
        session_id: "sess-1",
      });
      await openPromise;

      source.release("runtime-1");
      source.release("runtime-1");
      expect(frames.filter((f) => f.type === "unsubscribe")).toHaveLength(2);

      const reopen = source.open("runtime-1", { cols: 80, rows: 24 });
      emit("subscribe_ack", { scope: "terminal", id: "runtime-1" });
      await vi.waitFor(() =>
        expect(frames.filter((f) => f.type === "subscribe")).toHaveLength(2),
      );
      await vi.waitFor(() => expect(frames.at(-1)!.type).toBe("terminal.open"));
      emit("terminal.open_result", {
        req_id: frames.at(-1)!.payload.req_id,
        session_id: "sess-2",
      });
      const result = await reopen;
      expect(result.ok).toBe(true);
    });
  });
});
