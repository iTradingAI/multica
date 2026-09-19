// @vitest-environment jsdom
import type { ReactNode } from "react";
import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import { RESOURCES } from "@multica/views/locales";
import { FloatingTerminal } from "./floating-terminal";
import { useTerminalShortcut } from "@/hooks/use-terminal-shortcut";
import {
  defaultTerminalRect,
  parseTerminalRect,
  resolveTerminalRect,
  useTerminalStore,
} from "@/stores/terminal-store";

// ---------------------------------------------------------------------------
// xterm is a heavy DOM/canvas dependency whose rendering has nothing to do with
// what this suite covers: the panel's wiring to main. The mocks keep the
// API surface the panel actually uses and let the test drive both directions
// (pty → xterm writes, xterm → pty writes/resizes).
// ---------------------------------------------------------------------------

interface MockTerminal {
  cols: number;
  rows: number;
  options: Record<string, unknown>;
  written: string[];
  disposed: boolean;
  openedWith: HTMLElement | null;
  emitInput: (data: string) => void;
}

const xtermMock = vi.hoisted(() => {
  const instances: MockTerminal[] = [];
  const fitState = { cols: 80, rows: 24, fits: 0 };

  class Terminal {
    cols = 80;
    rows = 24;
    options: Record<string, unknown>;
    written: string[] = [];
    disposed = false;
    openedWith: HTMLElement | null = null;
    private inputHandler: ((data: string) => void) | null = null;

    constructor(options: Record<string, unknown>) {
      this.options = options;
      instances.push(this as unknown as MockTerminal);
    }

    loadAddon(addon: { attach?: (terminal: Terminal) => void }) {
      addon.attach?.(this);
    }

    open(element: HTMLElement) {
      this.openedWith = element;
    }

    write(data: string) {
      this.written.push(data);
    }

    focus() {}

    dispose() {
      this.disposed = true;
    }

    onData(handler: (data: string) => void) {
      this.inputHandler = handler;
      return {
        dispose: () => {
          this.inputHandler = null;
        },
      };
    }

    emitInput(data: string) {
      this.inputHandler?.(data);
    }
  }

  class FitAddon {
    private terminal: { cols: number; rows: number } | null = null;

    attach(terminal: { cols: number; rows: number }) {
      this.terminal = terminal;
    }

    fit() {
      fitState.fits += 1;
      if (!this.terminal) return;
      this.terminal.cols = fitState.cols;
      this.terminal.rows = fitState.rows;
    }
  }

  return { instances, fitState, Terminal, FitAddon };
});

vi.mock("@xterm/xterm", () => ({ Terminal: xtermMock.Terminal }));
vi.mock("@xterm/addon-fit", () => ({ FitAddon: xtermMock.FitAddon }));

// ---------------------------------------------------------------------------
// jsdom stubs
// ---------------------------------------------------------------------------

class ResizeObserverStub {
  static instances: ResizeObserverStub[] = [];
  private targets: Element[] = [];

  constructor(private readonly callback: ResizeObserverCallback) {
    ResizeObserverStub.instances.push(this);
  }

  observe(target: Element) {
    this.targets.push(target);
  }

  unobserve(target: Element) {
    this.targets = this.targets.filter((candidate) => candidate !== target);
  }

  disconnect() {
    this.targets = [];
  }

  observes(target: Element) {
    return this.targets.includes(target);
  }

  trigger() {
    this.callback([], this as unknown as ResizeObserver);
  }
}

function installTerminalAPI(
  spawnResult: { ok: true; sessionId: string } | { ok: false; reason: string } = {
    ok: true,
    sessionId: "11111111-1111-4111-8111-111111111111",
  },
) {
  const dataHandlers = new Set<(event: { sessionId: string; data: string }) => void>();
  const exitHandlers = new Set<(event: { sessionId: string; exitCode: number }) => void>();
  const api = {
    spawn: vi.fn(async () => spawnResult),
    write: vi.fn(),
    resize: vi.fn(),
    kill: vi.fn(),
    onData: vi.fn((handler: (event: { sessionId: string; data: string }) => void) => {
      dataHandlers.add(handler);
      return () => dataHandlers.delete(handler);
    }),
    onExit: vi.fn((handler: (event: { sessionId: string; exitCode: number }) => void) => {
      exitHandlers.add(handler);
      return () => exitHandlers.delete(handler);
    }),
  };
  Object.defineProperty(window, "terminalAPI", { configurable: true, value: api });
  return {
    api,
    emitData: (event: { sessionId: string; data: string }) => {
      for (const handler of dataHandlers) handler(event);
    },
    emitExit: (event: { sessionId: string; exitCode: number }) => {
      for (const handler of exitHandlers) handler(event);
    },
    dataSubscribers: () => dataHandlers.size,
    exitSubscribers: () => exitHandlers.size,
  };
}

/**
 * Installs a desktopAPI stub. `deliver()` plays the Ctrl+` message main sends
 * once the renderer has marked the channel ready.
 */
function installDesktopAPI(kind: "main" | "issue" = "main") {
  let handler: (() => void) | null = null;
  const onToggleTerminal = vi.fn((callback: () => void) => {
    handler = callback;
    return () => {
      handler = null;
    };
  });
  Object.defineProperty(window, "desktopAPI", {
    configurable: true,
    value: {
      windowContext:
        kind === "main"
          ? { kind: "main" }
          : { kind: "issue", path: "/acme/issues/abc", workspaceSlug: "acme" },
      onToggleTerminal,
    },
  });
  return { onToggleTerminal, deliver: () => handler?.() };
}

function renderPanel(element: ReactNode) {
  return render(
    <I18nProvider locale="en" resources={RESOURCES}>
      {element}
    </I18nProvider>,
  );
}

/** The hook plus the panel it drives, mounted the way the shell does it. */
function Shell() {
  useTerminalShortcut();
  return <FloatingTerminal />;
}

function panelElement(): HTMLElement {
  const element = document.querySelector<HTMLElement>('[data-slot="floating-terminal"]');
  if (!element) throw new Error("floating terminal panel is not rendered");
  return element;
}

function panelState(): string | null {
  return panelElement().getAttribute("data-state");
}

function bodyElement(): HTMLElement {
  const element = document.querySelector<HTMLElement>('[data-slot="floating-terminal-body"]');
  if (!element) throw new Error("floating terminal body is not rendered");
  return element;
}

function bodyObserver(): ResizeObserverStub {
  const observer = ResizeObserverStub.instances.find((candidate) =>
    candidate.observes(bodyElement()),
  );
  if (!observer) throw new Error("no ResizeObserver watching the terminal body");
  return observer;
}

/** jsdom lays every element out at zero size; the panel skips fitting then. */
function giveSize(element: HTMLElement, width: number, height: number) {
  Object.defineProperty(element, "clientWidth", { configurable: true, value: width });
  Object.defineProperty(element, "clientHeight", { configurable: true, value: height });
}

const SESSION_ID = "11111111-1111-4111-8111-111111111111";

beforeEach(() => {
  ResizeObserverStub.instances = [];
  vi.stubGlobal("ResizeObserver", ResizeObserverStub);
  xtermMock.instances.length = 0;
  xtermMock.fitState.cols = 80;
  xtermMock.fitState.rows = 24;
  xtermMock.fitState.fits = 0;
  useTerminalStore.setState({
    visible: false,
    rect: defaultTerminalRect(),
    activeSession: null,
  });
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("FloatingTerminal", () => {
  it("renders the panel closed, then opens it into a live pty session", async () => {
    const { api } = installTerminalAPI();
    installDesktopAPI();
    renderPanel(<FloatingTerminal />);

    expect(panelElement()).toBeInTheDocument();
    expect(panelState()).toBe("closed");
    expect(api.spawn).not.toHaveBeenCalled();
    // xterm is mounted with the shell, not with the first open — the panel is
    // only hidden while closed so scrollback and the shell survive a toggle.
    expect(xtermMock.instances).toHaveLength(1);
    expect(xtermMock.instances[0]?.openedWith).toBe(bodyElement());

    act(() => useTerminalStore.getState().setVisible(true));

    await waitFor(() => expect(api.spawn).toHaveBeenCalledTimes(1));
    expect(api.spawn).toHaveBeenCalledWith({ cols: 80, rows: 24 });
    expect(panelState()).toBe("open");
    await waitFor(() =>
      expect(useTerminalStore.getState().activeSession).toBe(SESSION_ID),
    );
  });

  it("toggles from the Ctrl+` message main delivers", async () => {
    const { api } = installTerminalAPI();
    const { deliver } = installDesktopAPI();
    renderPanel(<Shell />);

    act(() => deliver());
    await waitFor(() => expect(panelState()).toBe("open"));
    await waitFor(() => expect(api.spawn).toHaveBeenCalledTimes(1));

    act(() => deliver());
    expect(panelState()).toBe("closed");
    // Closing only hides the panel: the shell keeps running.
    expect(api.kill).not.toHaveBeenCalled();
    expect(useTerminalStore.getState().activeSession).toBe(SESSION_ID);
  });

  it("does not subscribe in a dedicated issue window", () => {
    installTerminalAPI();
    const { onToggleTerminal } = installDesktopAPI("issue");

    renderPanel(<Shell />);

    expect(onToggleTerminal).not.toHaveBeenCalled();
  });

  it("feeds pty output into xterm and xterm input back to the pty", async () => {
    const { api, emitData } = installTerminalAPI();
    installDesktopAPI();
    renderPanel(<FloatingTerminal />);
    act(() => useTerminalStore.getState().setVisible(true));
    await waitFor(() =>
      expect(useTerminalStore.getState().activeSession).toBe(SESSION_ID),
    );
    const terminal = xtermMock.instances[0];
    if (!terminal) throw new Error("xterm was not constructed");

    act(() => emitData({ sessionId: SESSION_ID, data: "hello\r\n" }));
    act(() => emitData({ sessionId: "other-session", data: "ignored" }));

    expect(terminal.written).toEqual(["hello\r\n"]);

    act(() => terminal.emitInput("ls\r"));
    expect(api.write).toHaveBeenCalledWith(SESSION_ID, "ls\r");
  });

  it("pushes a resized grid to the pty and clamps it to main's bounds", async () => {
    const { api } = installTerminalAPI();
    installDesktopAPI();
    renderPanel(<FloatingTerminal />);
    act(() => useTerminalStore.getState().setVisible(true));
    await waitFor(() =>
      expect(useTerminalStore.getState().activeSession).toBe(SESSION_ID),
    );
    expect(api.resize).not.toHaveBeenCalled();

    giveSize(bodyElement(), 900, 420);
    xtermMock.fitState.cols = 120;
    xtermMock.fitState.rows = 40;
    act(() => bodyObserver().trigger());
    expect(api.resize).toHaveBeenCalledWith(SESSION_ID, 120, 40);

    // addon-fit derives the grid from pixels, so an oversized panel can
    // propose a grid main rejects — the renderer trims it first.
    xtermMock.fitState.cols = 900;
    xtermMock.fitState.rows = 900;
    act(() => bodyObserver().trigger());
    expect(api.resize).toHaveBeenLastCalledWith(SESSION_ID, 500, 300);
  });

  it("clears the session on exit and restarts on demand", async () => {
    const { api, emitExit } = installTerminalAPI();
    installDesktopAPI();
    renderPanel(<FloatingTerminal />);
    act(() => useTerminalStore.getState().setVisible(true));
    await waitFor(() =>
      expect(useTerminalStore.getState().activeSession).toBe(SESSION_ID),
    );

    act(() => emitExit({ sessionId: SESSION_ID, exitCode: 0 }));

    expect(useTerminalStore.getState().activeSession).toBeNull();
    expect(screen.getByText("Shell exited (code 0)")).toBeInTheDocument();
    // A dead shell must not be respawned by the open effect; it waits for the
    // explicit restart.
    expect(api.spawn).toHaveBeenCalledTimes(1);
    expect(api.kill).not.toHaveBeenCalled();

    act(() => screen.getByRole("button", { name: "Restart shell" }).click());

    await waitFor(() => expect(api.spawn).toHaveBeenCalledTimes(2));
    await waitFor(() =>
      expect(useTerminalStore.getState().activeSession).toBe(SESSION_ID),
    );
    expect(screen.queryByText(/Shell exited/)).not.toBeInTheDocument();
  });

  it("reports a session that could not be spawned and offers a retry", async () => {
    const { api } = installTerminalAPI({ ok: false, reason: "spawn_failed" });
    installDesktopAPI();
    renderPanel(<FloatingTerminal />);

    act(() => useTerminalStore.getState().setVisible(true));

    expect(await screen.findByText("Could not start a shell.")).toBeInTheDocument();
    expect(useTerminalStore.getState().activeSession).toBeNull();

    act(() => screen.getByRole("button", { name: "Restart shell" }).click());
    await waitFor(() => expect(api.spawn).toHaveBeenCalledTimes(2));
  });

  it("kills the pty, unsubscribes and disposes xterm on unmount", async () => {
    const { api, dataSubscribers, exitSubscribers } = installTerminalAPI();
    installDesktopAPI();
    const { unmount } = renderPanel(<FloatingTerminal />);
    act(() => useTerminalStore.getState().setVisible(true));
    await waitFor(() =>
      expect(useTerminalStore.getState().activeSession).toBe(SESSION_ID),
    );
    expect(dataSubscribers()).toBe(1);
    expect(exitSubscribers()).toBe(1);

    unmount();

    // The renderer half of "a pty never outlives its window": main also kills
    // on WebContents destroy, but the panel must not leave one behind either.
    expect(api.kill).toHaveBeenCalledWith(SESSION_ID);
    expect(dataSubscribers()).toBe(0);
    expect(exitSubscribers()).toBe(0);
    expect(xtermMock.instances[0]?.disposed).toBe(true);
    expect(useTerminalStore.getState().activeSession).toBeNull();
  });
});

describe("terminal geometry", () => {
  it("anchors a panel with no saved position to the bottom-left inset", () => {
    expect(
      resolveTerminalRect(defaultTerminalRect(), { width: 1000, height: 600 }),
    ).toEqual({ x: 16, y: 224, width: 720, height: 360 });
  });

  it("clamps a saved rect back onto the canvas and above the minimum size", () => {
    expect(
      resolveTerminalRect(
        { x: 4000, y: 4000, width: 100, height: 100 },
        { width: 1000, height: 600 },
      ),
    ).toEqual({ x: 680, y: 420, width: 320, height: 180 });
  });

  it("keeps the requested geometry when the canvas cannot be measured", () => {
    expect(
      resolveTerminalRect({ x: 40, y: 60, width: 640, height: 320 }, null),
    ).toEqual({ x: 40, y: 60, width: 640, height: 320 });
  });

  it("rejects persisted geometry that is not usable", () => {
    expect(parseTerminalRect({ width: "wide" })).toBeNull();
    expect(parseTerminalRect([1, 2, 3])).toBeNull();
    expect(parseTerminalRect({ x: 10, y: 20, width: 640.4, height: 320.6 })).toEqual({
      x: 10,
      y: 20,
      width: 640,
      height: 321,
    });
  });
});
