// @vitest-environment node
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { BrowserWindow, WebContents } from "electron";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

type Handler = (...args: unknown[]) => void;
type IpcHandler = (...args: unknown[]) => unknown;

const ctx = vi.hoisted(() => ({
  handlers: new Map<string, Handler[]>(),
  ipcHandlers: new Map<string, IpcHandler>(),
  ipcHandle: vi.fn(),
  checkForUpdates: vi.fn(async () => ({
    updateInfo: { version: "0.3.18" },
    isUpdateAvailable: false,
  })),
  downloadUpdate: vi.fn(),
  quitAndInstall: vi.fn(),
  openExternal: vi.fn(),
  getVersion: vi.fn(() => "0.3.17"),
  userDataPath: "",
}));

vi.mock("electron-updater", () => {
  const autoUpdater = {
    autoDownload: false,
    autoInstallOnAppQuit: false,
    channel: undefined as string | undefined,
    allowDowngrade: false,
    on: vi.fn((event: string, handler: Handler) => {
      const handlers = ctx.handlers.get(event) ?? [];
      handlers.push(handler);
      ctx.handlers.set(event, handlers);
      return autoUpdater;
    }),
    checkForUpdates: ctx.checkForUpdates,
    downloadUpdate: ctx.downloadUpdate,
    quitAndInstall: ctx.quitAndInstall,
  };
  return { autoUpdater };
});

vi.mock("electron", () => ({
  app: {
    getVersion: ctx.getVersion,
    getPath: vi.fn(() => ctx.userDataPath),
  },
  BrowserWindow: class BrowserWindow {},
  shell: { openExternal: ctx.openExternal },
  ipcMain: {
    handle: ctx.ipcHandle,
  },
}));

import {
  configureMacX64UpdateChannel,
  macInstallerDownloadUrl,
  setupAutoUpdater,
} from "./updater";
import { updaterPreferencesPath } from "./updater-preferences";

describe("macOS x64 update channel", () => {
  it("does not touch established architecture paths", () => {
    for (const [platform, arch] of [
      ["darwin", "arm64"],
      ["win32", "x64"],
      ["win32", "arm64"],
      ["linux", "arm64"],
    ] as const) {
      const updater = { channel: null, allowDowngrade: true };

      configureMacX64UpdateChannel(updater, platform, arch);

      expect(updater).toEqual({ channel: null, allowDowngrade: true });
    }
  });

  it("does not enable downgrades when selecting an architecture feed", () => {
    const updater = { channel: null, allowDowngrade: true };

    configureMacX64UpdateChannel(updater, "darwin", "x64");

    expect(updater).toEqual({
      channel: "latest-x64",
      allowDowngrade: false,
    });
  });
});

describe("macOS manual installer URL", () => {
  it("uses the fixed fork release path for both supported architectures", () => {
    expect(macInstallerDownloadUrl("0.4.27", "x64")).toBe(
      "https://github.com/iTradingAI/multica/releases/download/v0.4.27/multica-desktop-0.4.27-mac-x64.dmg",
    );
    expect(macInstallerDownloadUrl("0.4.27", "arm64")).toBe(
      "https://github.com/iTradingAI/multica/releases/download/v0.4.27/multica-desktop-0.4.27-mac-arm64.dmg",
    );
  });

  it("rejects an invalid version or unsupported architecture", () => {
    expect(macInstallerDownloadUrl("../../untrusted", "arm64")).toBeNull();
    expect(macInstallerDownloadUrl("0.4.27", "ia32")).toBeNull();
  });
});

function emitUpdater(event: string, ...args: unknown[]) {
  for (const handler of ctx.handlers.get(event) ?? []) {
    handler(...args);
  }
}

async function invokeIpc(channel: string, ...args: unknown[]) {
  const handler = ctx.ipcHandlers.get(channel);
  if (!handler) throw new Error(`Missing IPC handler: ${channel}`);
  return handler({}, ...args);
}

function makeWindow() {
  const send = vi.fn();
  return {
    win: {
      isDestroyed: () => false,
      webContents: {
        isDestroyed: () => false,
        send,
      },
    } as unknown as BrowserWindow,
    send,
  };
}

function makeDestroyedWindow() {
  return {
    isDestroyed: () => true,
    get webContents(): WebContents {
      throw new TypeError("Object has been destroyed");
    },
  } as unknown as BrowserWindow;
}

function makeWindowWithDestroyedWebContents() {
  const send = vi.fn(() => {
    throw new TypeError("Object has been destroyed");
  });
  return {
    win: {
      isDestroyed: () => false,
      webContents: {
        isDestroyed: () => true,
        send,
      },
    } as unknown as BrowserWindow,
    send,
  };
}

function makeWindowWithThrowingSend(error: Error) {
  const send = vi.fn(() => {
    throw error;
  });
  return {
    win: {
      isDestroyed: () => false,
      webContents: {
        isDestroyed: () => false,
        send,
      },
    } as unknown as BrowserWindow,
    send,
  };
}

describe("setupAutoUpdater", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    ctx.userDataPath = mkdtempSync(join(tmpdir(), "multica-updater-test-"));
    ctx.handlers.clear();
    ctx.ipcHandlers.clear();
    ctx.ipcHandle.mockClear();
    ctx.ipcHandle.mockImplementation((channel: string, handler: IpcHandler) => {
      ctx.ipcHandlers.set(channel, handler);
    });
    ctx.checkForUpdates.mockClear();
    ctx.downloadUpdate.mockClear();
    ctx.quitAndInstall.mockClear();
    ctx.openExternal.mockReset().mockResolvedValue(undefined);
    ctx.getVersion.mockClear();
  });

  afterEach(() => {
    vi.unstubAllEnvs();
    vi.clearAllTimers();
    vi.useRealTimers();
    rmSync(ctx.userDataPath, { recursive: true, force: true });
  });

  it("enables automatic background updates by default", async () => {
    setupAutoUpdater(() => null);

    await expect(invokeIpc("updater:get-preferences")).resolves.toEqual({
      automaticUpdates: true,
    });

    await vi.advanceTimersByTimeAsync(5_000);
    expect(ctx.checkForUpdates).toHaveBeenCalledTimes(1);
  });

  it("skips startup and periodic checks when automatic updates are disabled", async () => {
    writeFileSync(
      updaterPreferencesPath(ctx.userDataPath),
      JSON.stringify({ automaticUpdates: false }),
    );
    setupAutoUpdater(() => null);

    // Let the async preference load settle before advancing timers; otherwise
    // the in-flight readFile can resolve after afterEach() removes the temp
    // dir, default back to enabled=true, and fire a background check into the
    // next test's freshly-cleared mock (flake on slow CI).
    await invokeIpc("updater:get-preferences");

    await vi.advanceTimersByTimeAsync(60 * 60 * 1000 + 5_000);

    expect(ctx.checkForUpdates).not.toHaveBeenCalled();
  });

  it("persists the automatic update preference and stops future background checks", async () => {
    setupAutoUpdater(() => null);

    await expect(
      invokeIpc("updater:set-automatic-updates", false),
    ).resolves.toEqual({ automaticUpdates: false });
    expect(
      JSON.parse(
        readFileSync(updaterPreferencesPath(ctx.userDataPath), "utf-8"),
      ),
    ).toEqual({ automaticUpdates: false });

    await vi.advanceTimersByTimeAsync(60 * 60 * 1000 + 5_000);
    expect(ctx.checkForUpdates).not.toHaveBeenCalled();
  });

  it("still allows an explicit manual check when automatic updates are disabled", async () => {
    writeFileSync(
      updaterPreferencesPath(ctx.userDataPath),
      JSON.stringify({ automaticUpdates: false }),
    );
    setupAutoUpdater(() => null);

    await expect(invokeIpc("updater:check")).resolves.toMatchObject({
      ok: true,
    });

    expect(ctx.checkForUpdates).toHaveBeenCalledTimes(1);
  });

  it("forwards update progress to a live renderer", () => {
    const { win, send } = makeWindow();
    setupAutoUpdater(() => win);

    emitUpdater("download-progress", { percent: 42 });

    expect(send).toHaveBeenCalledWith("updater:download-progress", {
      percent: 42,
    });
  });

  it("opens the matching official installer for a macOS update", async () => {
    const originalPlatform = Object.getOwnPropertyDescriptor(process, "platform");
    if (!originalPlatform) throw new Error("process.platform is unavailable");

    Object.defineProperty(process, "platform", {
      value: "darwin",
      configurable: true,
    });
    try {
      const { win, send } = makeWindow();
      setupAutoUpdater(() => win);

      emitUpdater("update-available", {
        version: "0.4.27",
      });

      expect(send).toHaveBeenCalledWith("updater:update-available", {
        version: "0.4.27",
        releaseNotes: undefined,
        manualInstall: true,
      });
      await invokeIpc("updater:install");
      expect(ctx.openExternal).toHaveBeenCalledWith(
        "https://github.com/iTradingAI/multica/releases/download/v0.4.27/multica-desktop-0.4.27-mac-x64.dmg",
      );
      expect(ctx.quitAndInstall).not.toHaveBeenCalled();
    } finally {
      Object.defineProperty(process, "platform", originalPlatform);
    }
  });

  it("does not open a macOS installer for an invalid update version", async () => {
    const originalPlatform = Object.getOwnPropertyDescriptor(process, "platform");
    if (!originalPlatform) throw new Error("process.platform is unavailable");

    Object.defineProperty(process, "platform", {
      value: "darwin",
      configurable: true,
    });
    try {
      setupAutoUpdater(() => null);
      emitUpdater("update-available", { version: "../../untrusted" });

      ctx.checkForUpdates.mockResolvedValueOnce({
        updateInfo: { version: "../../untrusted" },
        isUpdateAvailable: true,
      });
      await expect(invokeIpc("updater:check")).resolves.toEqual({
        ok: false,
        error: "No compatible macOS update is available",
      });
      await expect(invokeIpc("updater:install")).rejects.toThrow(
        "No compatible macOS update is available",
      );
      expect(ctx.openExternal).not.toHaveBeenCalled();
      expect(ctx.quitAndInstall).not.toHaveBeenCalled();
    } finally {
      Object.defineProperty(process, "platform", originalPlatform);
    }
  });

  it("keeps a checked macOS update available for the manual installer action", async () => {
    const originalPlatform = Object.getOwnPropertyDescriptor(process, "platform");
    if (!originalPlatform) throw new Error("process.platform is unavailable");

    Object.defineProperty(process, "platform", {
      value: "darwin",
      configurable: true,
    });
    try {
      ctx.checkForUpdates.mockResolvedValueOnce({
        updateInfo: { version: "0.4.27" },
        isUpdateAvailable: true,
      });
      setupAutoUpdater(() => null);

      await expect(invokeIpc("updater:check")).resolves.toMatchObject({
        ok: true,
        latestVersion: "0.4.27",
        available: true,
        manualInstall: true,
      });
      ctx.openExternal.mockClear();
      await invokeIpc("updater:download");

      expect(ctx.openExternal).toHaveBeenCalledWith(
        "https://github.com/iTradingAI/multica/releases/download/v0.4.27/multica-desktop-0.4.27-mac-x64.dmg",
      );
      expect(ctx.downloadUpdate).not.toHaveBeenCalled();
      ctx.openExternal.mockClear();
      await invokeIpc("updater:install");

      expect(ctx.openExternal).toHaveBeenCalledWith(
        "https://github.com/iTradingAI/multica/releases/download/v0.4.27/multica-desktop-0.4.27-mac-x64.dmg",
      );
      expect(ctx.quitAndInstall).not.toHaveBeenCalled();
    } finally {
      Object.defineProperty(process, "platform", originalPlatform);
    }
  });

  it("skips update progress when the BrowserWindow has already been destroyed", () => {
    setupAutoUpdater(() => makeDestroyedWindow());

    expect(() => emitUpdater("download-progress", { percent: 42 })).not.toThrow();
  });

  it("skips update progress when the BrowserWindow webContents has already been destroyed", () => {
    const { win, send } = makeWindowWithDestroyedWebContents();
    setupAutoUpdater(() => win);

    expect(() => emitUpdater("download-progress", { percent: 42 })).not.toThrow();
    expect(send).not.toHaveBeenCalled();
  });

  it("skips update progress when webContents.send loses a destroy race", () => {
    const { win, send } = makeWindowWithThrowingSend(
      new TypeError("Object has been destroyed"),
    );
    setupAutoUpdater(() => win);

    expect(() => emitUpdater("download-progress", { percent: 42 })).not.toThrow();
    expect(send).toHaveBeenCalledWith("updater:download-progress", {
      percent: 42,
    });
  });

  it("rethrows non-destroy errors from webContents.send", () => {
    const { win } = makeWindowWithThrowingSend(new Error("boom"));
    setupAutoUpdater(() => win);

    expect(() => emitUpdater("download-progress", { percent: 42 })).toThrow(
      "boom",
    );
  });
});
