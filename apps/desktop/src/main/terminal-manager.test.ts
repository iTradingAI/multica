// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  TerminalManager,
  type PtyProcess,
  type PtySpawn,
  type TerminalOwner,
} from "./terminal-manager";
import { TERMINAL_MAX_SESSIONS } from "../shared/terminal-types";

/** Recordable stand-in for a node-pty process. */
class FakePty implements PtyProcess {
  readonly pid: number;
  written: string[] = [];
  resized: { cols: number; rows: number }[] = [];
  killed = false;
  private dataListeners: ((data: string) => void)[] = [];
  private exitListeners: ((event: { exitCode: number }) => void)[] = [];

  constructor(pid: number) {
    this.pid = pid;
  }

  write(data: string): void {
    this.written.push(data);
  }

  resize(cols: number, rows: number): void {
    this.resized.push({ cols, rows });
  }

  kill(): void {
    this.killed = true;
  }

  onData(listener: (data: string) => void): { dispose(): void } {
    this.dataListeners.push(listener);
    return { dispose: () => {} };
  }

  onExit(
    listener: (event: { exitCode: number }) => void,
  ): { dispose(): void } {
    this.exitListeners.push(listener);
    return { dispose: () => {} };
  }

  emitData(data: string): void {
    for (const listener of this.dataListeners) listener(data);
  }

  emitExit(exitCode = 0): void {
    for (const listener of this.exitListeners) listener({ exitCode });
  }
}

/** Stand-in for a WebContents owning sessions. */
class FakeOwner implements TerminalOwner {
  destroyed = false;
  sent: { channel: string; payload: unknown }[] = [];
  private destroyedListeners: (() => void)[] = [];

  send(channel: "terminal:data" | "terminal:exit", payload: unknown): void {
    if (this.destroyed) throw new Error("send after destroy");
    this.sent.push({ channel, payload });
  }

  isDestroyed(): boolean {
    return this.destroyed;
  }

  onDestroyed(listener: () => void): () => void {
    this.destroyedListeners.push(listener);
    return () => {
      this.destroyedListeners = this.destroyedListeners.filter(
        (l) => l !== listener,
      );
    };
  }

  emitDestroyed(): void {
    this.destroyed = true;
    for (const listener of [...this.destroyedListeners]) listener();
  }
}

function makeManager(overrides?: {
  spawnPty?: PtySpawn;
  isDirectory?: (path: string) => boolean;
  isExecutableFile?: (path: string) => boolean;
}) {
  const ptys: FakePty[] = [];
  const spawnPty: PtySpawn =
    overrides?.spawnPty ??
    (() => {
      const pty = new FakePty(ptys.length + 1);
      ptys.push(pty);
      return pty;
    });
  const manager = new TerminalManager({
    spawnPty,
    resolveShell: () => ({ file: "/bin/bash" }),
    isDirectory: overrides?.isDirectory ?? (() => true),
    isExecutableFile: overrides?.isExecutableFile ?? (() => true),
  });
  return { manager, ptys };
}

const spawnRequest = { cols: 80, rows: 24 };

describe("TerminalManager.createSession", () => {
  it("spawns through the injected factory and returns a session id", () => {
    const { manager, ptys } = makeManager();
    const result = manager.createSession(spawnRequest, new FakeOwner());
    expect(result.ok).toBe(true);
    expect(ptys).toHaveLength(1);
    if (!result.ok) return;
    expect(result.sessionId).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/,
    );
  });

  it("passes cols, rows, cwd, and shell override to the pty", () => {
    const seen: unknown[] = [];
    const { manager } = makeManager({
      spawnPty: (options) => {
        seen.push(options);
        return new FakePty(1);
      },
    });
    manager.createSession(
      { cols: 120, rows: 40, cwd: "/tmp", shell: "/usr/local/bin/claude" },
      new FakeOwner(),
    );
    expect(seen).toEqual([
      { file: "/usr/local/bin/claude", cwd: "/tmp", cols: 120, rows: 40 },
    ]);
  });

  it("uses the resolved default shell when no override is given", () => {
    const seen: unknown[] = [];
    const { manager } = makeManager({
      spawnPty: (options) => {
        seen.push(options);
        return new FakePty(1);
      },
    });
    manager.createSession(spawnRequest, new FakeOwner());
    expect(seen).toEqual([{ file: "/bin/bash", cols: 80, rows: 24 }]);
  });

  it("rejects a cwd that is not an existing directory", () => {
    const { manager } = makeManager({ isDirectory: () => false });
    const result = manager.createSession(
      { ...spawnRequest, cwd: "/nope" },
      new FakeOwner(),
    );
    expect(result).toEqual({
      ok: false,
      reason: "invalid_request",
      message: "cwd is not an existing directory",
    });
  });

  it("rejects a shell override that is not an existing file", () => {
    const { manager } = makeManager({ isExecutableFile: () => false });
    const result = manager.createSession(
      { ...spawnRequest, shell: "/nope/claude" },
      new FakeOwner(),
    );
    expect(result).toEqual({
      ok: false,
      reason: "invalid_request",
      message: "shell is not an existing executable file",
    });
  });

  it("caps concurrent sessions", () => {
    const { manager } = makeManager();
    for (let i = 0; i < TERMINAL_MAX_SESSIONS; i++) {
      expect(manager.createSession(spawnRequest, new FakeOwner()).ok).toBe(
        true,
      );
    }
    const result = manager.createSession(spawnRequest, new FakeOwner());
    expect(result).toMatchObject({ ok: false, reason: "limit_reached" });
    expect(manager.sessionCount()).toBe(TERMINAL_MAX_SESSIONS);
  });

  it("reports spawn failures without throwing", () => {
    const { manager } = makeManager({
      spawnPty: () => {
        throw new Error("fork failed");
      },
    });
    const result = manager.createSession(spawnRequest, new FakeOwner());
    expect(result).toEqual({
      ok: false,
      reason: "spawn_failed",
      message: "fork failed",
    });
    expect(manager.sessionCount()).toBe(0);
  });
});

describe("TerminalManager events", () => {
  it("routes pty output to the owning renderer with the session id", () => {
    const { manager, ptys } = makeManager();
    const owner = new FakeOwner();
    const result = manager.createSession(spawnRequest, owner);
    if (!result.ok) throw new Error("spawn failed");
    ptys[0].emitData("hello");
    expect(owner.sent).toEqual([
      { channel: "terminal:data", payload: { sessionId: result.sessionId, data: "hello" } },
    ]);
  });

  it("emits exit, then drops the session", () => {
    const { manager, ptys } = makeManager();
    const owner = new FakeOwner();
    const result = manager.createSession(spawnRequest, owner);
    if (!result.ok) throw new Error("spawn failed");
    ptys[0].emitExit(127);
    expect(owner.sent).toEqual([
      { channel: "terminal:exit", payload: { sessionId: result.sessionId, exitCode: 127 } },
    ]);
    expect(manager.sessionCount()).toBe(0);
    // Writes to a dead session are dropped, not thrown.
    expect(manager.write({ sessionId: result.sessionId, data: "x" })).toBe(
      false,
    );
  });

  it("stops emitting data for an exited session", () => {
    const { manager, ptys } = makeManager();
    const owner = new FakeOwner();
    const result = manager.createSession(spawnRequest, owner);
    if (!result.ok) throw new Error("spawn failed");
    ptys[0].emitExit(0);
    const sent = owner.sent.length;
    ptys[0].emitData("stale");
    expect(owner.sent).toHaveLength(sent);
  });
});

describe("TerminalManager lifecycle", () => {
  it("write and resize reach the pty", () => {
    const { manager, ptys } = makeManager();
    const result = manager.createSession(spawnRequest, new FakeOwner());
    if (!result.ok) throw new Error("spawn failed");
    expect(
      manager.write({ sessionId: result.sessionId, data: "ls\r" }),
    ).toBe(true);
    expect(ptys[0].written).toEqual(["ls\r"]);
    expect(
      manager.resize({ sessionId: result.sessionId, cols: 100, rows: 30 }),
    ).toBe(true);
    expect(ptys[0].resized).toEqual([{ cols: 100, rows: 30 }]);
  });

  it("kill terminates the session exactly once", () => {
    const { manager, ptys } = makeManager();
    const result = manager.createSession(spawnRequest, new FakeOwner());
    if (!result.ok) throw new Error("spawn failed");
    expect(manager.kill(result.sessionId)).toBe(true);
    expect(ptys[0].killed).toBe(true);
    expect(manager.sessionCount()).toBe(0);
    expect(manager.kill(result.sessionId)).toBe(false);
  });

  it("kills its sessions when the owning window is destroyed", () => {
    const { manager, ptys } = makeManager();
    const owner = new FakeOwner();
    const result = manager.createSession(spawnRequest, owner);
    if (!result.ok) throw new Error("spawn failed");
    owner.emitDestroyed();
    expect(ptys[0].killed).toBe(true);
    expect(manager.sessionCount()).toBe(0);
  });

  it("disposeAll kills every session and tolerates re-entry", () => {
    const { manager, ptys } = makeManager();
    manager.createSession(spawnRequest, new FakeOwner());
    manager.createSession(spawnRequest, new FakeOwner());
    expect(manager.sessionCount()).toBe(2);
    manager.disposeAll();
    expect(ptys.map((p) => p.killed)).toEqual([true, true]);
    expect(manager.sessionCount()).toBe(0);
    expect(() => manager.disposeAll()).not.toThrow();
  });

  it("does not double-kill when the owner is destroyed after an exit", () => {
    const { manager, ptys } = makeManager();
    const owner = new FakeOwner();
    const result = manager.createSession(spawnRequest, owner);
    if (!result.ok) throw new Error("spawn failed");
    ptys[0].emitExit(0);
    owner.emitDestroyed();
    expect(ptys[0].killed).toBe(false);
  });

  it("ignores data callbacks for a destroyed owner", () => {
    const { manager, ptys } = makeManager();
    const owner = new FakeOwner();
    const result = manager.createSession(spawnRequest, owner);
    if (!result.ok) throw new Error("spawn failed");
    owner.emitDestroyed();
    // Late pty output after teardown must not reach a dead WebContents.
    expect(() => ptys[0].emitData("late")).not.toThrow();
    expect(owner.sent).toHaveLength(0);
  });
});
