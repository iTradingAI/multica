import { ipcMain } from "electron";
import { spawn as nodePtySpawn } from "node-pty";
import { TerminalManager, type PtyProcess } from "./terminal-manager";
import {
  fsIsDirectory,
  fsIsExecutableFile,
  resolveLocalDefaultShell,
  shellEnv,
} from "./terminal-shell";
import {
  parseTerminalKillInput,
  parseTerminalResizeInput,
  parseTerminalSpawnRequest,
  parseTerminalWriteInput,
  type TerminalSpawnResult,
} from "../shared/terminal-types";

/**
 * Electron + node-pty wiring for the floating terminal. This is the only
 * module in the terminal stack that imports the native addon, so the unit
 * tests for manager/shell logic never load it.
 *
 * Sessions are owned by the WebContents that spawned them: output and exit
 * events are sent back to that exact renderer, and a destroyed WebContents
 * kills its ptys via the manager's owner-teardown hook.
 */
export function setupTerminalIpc(): void {
  const manager = new TerminalManager({
    spawnPty: (options): PtyProcess => {
      // IPty is structurally compatible with PtyProcess; the adapter just
      // forwards the shared spawn options into node-pty's argument shape.
      return nodePtySpawn(options.file, options.args ?? [], {
        name: "xterm-256color",
        cols: options.cols,
        rows: options.rows,
        ...(options.cwd !== undefined ? { cwd: options.cwd } : {}),
        env: shellEnv(),
      }) as unknown as PtyProcess;
    },
    resolveShell: () => resolveLocalDefaultShell(),
    isDirectory: fsIsDirectory,
    isExecutableFile: fsIsExecutableFile,
  });

  ipcMain.handle(
    "terminal:spawn",
    (
      event: Electron.IpcMainInvokeEvent,
      request: unknown,
    ): TerminalSpawnResult => {
      if (event.sender.isDestroyed()) {
        return { ok: false, reason: "invalid_request" };
      }
      const parsed = parseTerminalSpawnRequest(request);
      if (!parsed) {
        return { ok: false, reason: "invalid_request" };
      }
      return manager.createSession(parsed, ownerFrom(event.sender));
    },
  );

  // The remote `terminal.open` payload needs the same default shell the
  // local pty host would pick; the renderer has no filesystem access, so it
  // asks main once per remote open.
  ipcMain.handle("terminal:default-shell", () => {
    const shell = resolveLocalDefaultShell();
    return { file: shell.file, args: shell.args };
  });

  // Fire-and-forget channels: the renderer already holds the session, so a
  // dropped message (window mid-teardown, unknown id) needs no reply.
  ipcMain.on("terminal:write", (event, input: unknown) => {
    if (event.sender.isDestroyed()) return;
    const parsed = parseTerminalWriteInput(input);
    if (!parsed) return;
    manager.write(parsed);
  });

  ipcMain.on("terminal:resize", (event, input: unknown) => {
    if (event.sender.isDestroyed()) return;
    const parsed = parseTerminalResizeInput(input);
    if (!parsed) return;
    manager.resize(parsed);
  });

  ipcMain.on("terminal:kill", (event, input: unknown) => {
    if (event.sender.isDestroyed()) return;
    const parsed = parseTerminalKillInput(input);
    if (!parsed) return;
    manager.kill(parsed.sessionId);
  });
}

/**
 * Adapt an Electron WebContents into the manager's TerminalOwner shape.
 * "destroyed" covers close, crash, and renderer-gone scenarios.
 */
function ownerFrom(sender: Electron.WebContents) {
  return {
    send: (
      channel: "terminal:data" | "terminal:exit",
      payload: unknown,
    ): void => {
      if (!sender.isDestroyed()) sender.send(channel, payload);
    },
    isDestroyed: () => sender.isDestroyed(),
    onDestroyed: (listener: () => void): (() => void) => {
      sender.on("destroyed", listener);
      return () => {
        sender.removeListener("destroyed", listener);
      };
    },
  };
}
