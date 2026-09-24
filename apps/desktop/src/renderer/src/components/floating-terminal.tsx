import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type PointerEvent as ReactPointerEvent,
  type RefObject,
  type ReactNode,
} from "react";
import {
  DndContext,
  PointerSensor,
  useDraggable,
  useSensor,
  useSensors,
  type DragEndEvent,
} from "@dnd-kit/core";
import { CSS } from "@dnd-kit/utilities";
import { FitAddon } from "@xterm/addon-fit";
import { Terminal, type ITheme } from "@xterm/xterm";
import { Loader2, RotateCcw, SquareTerminal, X } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@multica/ui/components/ui/button";
import { useTheme } from "@multica/ui/components/common/theme-provider";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "@multica/views/i18n";
import { useWS } from "@multica/core/realtime";
// The renderer owns xterm's stylesheet; the package ships the same file for
// bundlers that do not auto-inject it.
import "@xterm/xterm/css/xterm.css";
import {
  TERMINAL_MAX_COLS,
  TERMINAL_MAX_ROWS,
  TERMINAL_MIN_COLS,
  TERMINAL_MIN_ROWS,
} from "../../../shared/terminal-types";
import {
  resolveTerminalRect,
  useTerminalStore,
  type ResolvedTerminalRect,
  type TerminalCanvasBounds,
} from "@/stores/terminal-store";
import {
  useTerminalMachines,
  type TerminalMachine,
} from "@/terminal/machines";
import { RemoteSessionSource } from "@/terminal/remote-session";
import {
  terminalErrorKey,
  type TerminalExitInfo,
  type TerminalSessionSource,
  type TerminalTarget,
} from "@/terminal/session-source";

const PANEL_DRAG_ID = "floating-terminal-panel";

/**
 * xterm theme slots mapped to CSS design tokens. Reading the tokens instead of
 * naming colours means the terminal follows the app's dark/light theme with no
 * second palette to keep in sync — and "no hardcoded colour" is enforced by
 * construction, since the values only ever come from the live stylesheet.
 */
const TERMINAL_THEME_TOKENS = {
  background: "--background",
  foreground: "--foreground",
  cursor: "--foreground",
  cursorAccent: "--background",
  selectionBackground: "--surface-selected",
  black: "--app-shell",
  brightBlack: "--muted-foreground",
  red: "--destructive",
  yellow: "--warning",
  green: "--success",
  blue: "--info",
  brightWhite: "--foreground",
} as const;

/** Font family token; it names the bundled Geist Mono face (see globals.css). */
const TERMINAL_FONT_TOKEN = "--font-mono";

const TERMINAL_FONT_SIZE = 12;
const TERMINAL_SCROLLBACK = 5000;

function readToken(element: HTMLElement, token: string): string {
  return getComputedStyle(element).getPropertyValue(token).trim();
}

/**
 * Build an xterm theme from the panel's computed styles. Slots whose token is
 * missing (unstyled document, first paint) are omitted so xterm keeps its own
 * default rather than receiving a hardcoded fallback.
 */
export function readTerminalTheme(element: HTMLElement): ITheme {
  const palette: ITheme = {};
  for (const [slot, token] of Object.entries(TERMINAL_THEME_TOKENS)) {
    const value = readToken(element, token);
    if (value) (palette as Record<string, string>)[slot] = value;
  }
  return palette;
}

/**
 * Clamp a grid to the bounds main enforces (see shared/terminal-types.ts).
 * addon-fit derives cols/rows from pixels, so a very large or very small panel
 * can propose a size main would reject; the renderer trims it first and the
 * resize never silently disappears.
 */
function clampGrid(cols: number, rows: number): { cols: number; rows: number } {
  const clamp = (value: number, min: number, max: number) =>
    Math.max(min, Math.min(max, Math.round(value)));
  return {
    cols: clamp(cols, TERMINAL_MIN_COLS, TERMINAL_MAX_COLS),
    rows: clamp(rows, TERMINAL_MIN_ROWS, TERMINAL_MAX_ROWS),
  };
}

type ResizeDirection = "right" | "bottom" | "corner";

interface TerminalPanelProps {
  panelRef: RefObject<HTMLDivElement | null>;
  hostRef: RefObject<HTMLDivElement | null>;
  resolved: ResolvedTerminalRect;
  visible: boolean;
  exitCode: number | null;
  spawnFailed: boolean;
  canRestart: boolean;
  machines: TerminalMachine[];
  target: TerminalTarget;
  localHidden: boolean;
  onTargetChange: (target: TerminalTarget) => void;
  onHide: () => void;
  onRestart: () => void;
  children?: ReactNode;
  onResizeStart: (
    event: ReactPointerEvent<HTMLDivElement>,
    direction: ResizeDirection,
  ) => void;
}

/**
 * Panel chrome. Split from FloatingTerminal only because `useDraggable` must
 * run below the `DndContext` that FloatingTerminal renders — the hook reads the
 * context, so calling it in the same component that provides the context would
 * not see it.
 */
function TerminalPanel({
  panelRef,
  hostRef,
  resolved,
  visible,
  exitCode,
  spawnFailed,
  canRestart,
  machines,
  target,
  localHidden,
  onTargetChange,
  onHide,
  onRestart,
  children,
  onResizeStart,
}: TerminalPanelProps) {
  const { t } = useT("settings");
  // Only a pointer sensor is wired: the panel is dragged by its title bar, and
  // dnd-kit's default KeyboardSensor would announce arrow-key dragging on a
  // handle that does not implement it.
  const { listeners, setNodeRef, transform, isDragging } = useDraggable({
    id: PANEL_DRAG_ID,
  });

  const attachPanel = useCallback(
    (node: HTMLDivElement | null) => {
      panelRef.current = node;
      setNodeRef(node);
    },
    [panelRef, setNodeRef],
  );

  return (
    <div
      ref={attachPanel}
      data-slot="floating-terminal"
      data-state={visible ? "open" : "closed"}
      role="dialog"
      aria-label={t(($) => $.desktop.terminal.title)}
      className={cn(
        // z-40 keeps the panel above tab content but below window-level
        // takeovers (WindowOverlay), which are meant to own the window.
        "absolute z-40 flex-col overflow-hidden rounded-xl bg-surface text-surface-foreground ring-1 ring-surface-border shadow-[var(--floating-shadow)]",
        visible ? "flex" : "hidden",
        isDragging && "cursor-grabbing",
      )}
      style={{
        left: resolved.x,
        top: resolved.y,
        width: resolved.width,
        height: resolved.height,
        // dnd-kit reports the in-flight offset; the committed position is
        // written to the store on drag end, so the next render needs no
        // transform at all.
        transform: CSS.Translate.toString(transform),
      }}
    >
      <header
        // A plain drag handle, not a button: see the sensor comment above.
        {...listeners}
        data-slot="floating-terminal-header"
        className="flex h-8 shrink-0 cursor-grab items-center gap-2 border-b border-surface-border px-2 select-none"
      >
        <SquareTerminal aria-hidden className="size-3.5 text-muted-foreground" />
        <span className="truncate text-xs font-medium">
          {t(($) => $.desktop.terminal.title)}
        </span>
        {/* Target picker: the local machine plus every runtime advertising
            the terminal capability. The list is plain <select> so it stays
            keyboard-accessible without dragging in a popover into a
            drag-handle header. */}
        <select
          data-slot="floating-terminal-target"
          aria-label={t(($) => $.desktop.terminal.target_machine)}
          className="h-5 max-w-40 rounded border border-surface-border bg-background px-1 text-xs text-foreground"
          onChange={(event) => onTargetChange(event.target.value)}
          onPointerDown={(event) => event.stopPropagation()}
          value={target}
        >
          <option value="local">{t(($) => $.desktop.terminal.local_machine)}</option>
          {machines.map((machine) => (
            <option key={machine.id} value={machine.id}>
              {machine.label}
            </option>
          ))}
        </select>
        <span className="flex-1" />
        {canRestart && (
          <Button
            aria-label={t(($) => $.desktop.terminal.restart)}
            className="size-6"
            onClick={onRestart}
            // The header owns the drag gesture; the button must not start one.
            onPointerDown={(event) => event.stopPropagation()}
            size="icon"
            type="button"
            variant="ghost"
          >
            <RotateCcw className="size-3.5" />
          </Button>
        )}
        <Button
          aria-label={t(($) => $.desktop.terminal.hide)}
          className="size-6"
          onClick={onHide}
          onPointerDown={(event) => event.stopPropagation()}
          size="icon"
          type="button"
          variant="ghost"
        >
          <X className="size-3.5" />
        </Button>
      </header>

      {/* xterm mounts here. Its own background stays transparent so the theme
          set on the Terminal instance is what paints. Kept mounted while a
          remote tab is active so the local shell's scrollback survives. */}
      <div
        ref={hostRef}
        data-slot="floating-terminal-body"
        className="min-h-0 flex-1 overflow-hidden bg-background"
        style={localHidden ? { display: "none" } : undefined}
      />

      {(exitCode !== null || spawnFailed) && !localHidden && (
        <p
          data-slot="floating-terminal-status"
          className="shrink-0 border-t border-surface-border px-2 py-1 text-[11px] text-muted-foreground"
        >
          {spawnFailed
            ? t(($) => $.desktop.terminal.spawn_failed)
            : t(($) => $.desktop.terminal.exited, { code: exitCode })}
        </p>
      )}

      {/* Remote tabs coexist with the local shell; each manages its own
          visibility so a background tab keeps its scrollback and session. */}
      {children}

      {/* Edge targets grow the panel right/down; it is anchored top-left. */}
      <div
        aria-hidden
        className="absolute inset-y-0 right-0 z-10 w-1 cursor-col-resize"
        onPointerDown={(event) => onResizeStart(event, "right")}
      />
      <div
        aria-hidden
        className="absolute inset-x-0 bottom-0 z-10 h-1 cursor-row-resize"
        onPointerDown={(event) => onResizeStart(event, "bottom")}
      />
      <div
        aria-hidden
        className="absolute right-0 bottom-0 z-20 size-3 cursor-nwse-resize"
        onPointerDown={(event) => onResizeStart(event, "corner")}
      />
    </div>
  );
}

/**
 * One remote tab: an xterm wired to a session on `runtimeId` through the
 * realtime-hub terminal relay. Lives for as long as the tab is open (hidden,
 * not unmounted, while another tab is active) so scrollback and the remote
 * shell survive tab switches.
 */
function RemoteTerminalView({
  runtimeId,
  label,
  source,
  active,
  onDaemonOffline,
}: {
  runtimeId: string;
  label: string;
  source: TerminalSessionSource;
  active: boolean;
  onDaemonOffline: (runtimeId: string) => void;
}) {
  const { t } = useT("settings");
  const hostRef = useRef<HTMLDivElement | null>(null);
  const handleRef = useRef<{ session: string } | null>(null);
  const [status, setStatus] = useState<
    | { state: "connecting" }
    | { state: "ready" }
    | { state: "error"; errorKey: string }
    | { state: "exited"; code?: number }
  >({ state: "connecting" });

  useEffect(() => {
    const host = hostRef.current;
    if (!host) return undefined;
    const terminal = new Terminal({
      fontSize: TERMINAL_FONT_SIZE,
      fontFamily: readToken(host, TERMINAL_FONT_TOKEN) || undefined,
      scrollback: TERMINAL_SCROLLBACK,
      cursorBlink: true,
      theme: readTerminalTheme(host),
    });
    const fit = new FitAddon();
    terminal.loadAddon(fit);
    terminal.open(host);
    const grid = clampGrid(terminal.cols, terminal.rows);
    let disposed = false;
    let offs: Array<() => void> = [];

    void source.open(runtimeId, { cols: grid.cols, rows: grid.rows }).then(
      (result) => {
        if (!result.ok) {
          setStatus({ state: "error", errorKey: terminalErrorKey(result.error) });
          // An errored tab holds no session; free the exclusive scope so the
          // machine is not blocked for other clients until this app exits.
          source.release?.(runtimeId);
          return;
        }
        if (disposed) {
          // The tab went away while the relay was opening; the remote pty
          // must not be left running behind a dead view.
          source.kill(result.handle.session);
          return;
        }
        handleRef.current = result.handle;
        offs.push(result.handle.onData((data) => terminal.write(data)));
        offs.push(
          result.handle.onExit((exit: TerminalExitInfo) => {
            handleRef.current = null;
            // The session is gone; release the exclusive scope. Re-opening
            // ("restart shell" or a new tab) re-subscribes on demand.
            source.release?.(runtimeId);
            if (exit.reason === "daemon_offline") {
              setStatus({ state: "error", errorKey: "err_offline" });
              toast(t(($) => $.desktop.terminal.daemon_offline, { machine: label }));
              onDaemonOffline(runtimeId);
              return;
            }
            setStatus({ state: "exited", code: exit.code });
          }),
        );
        setStatus({ state: "ready" });
        terminal.focus();
      },
    );

    const offInput = terminal.onData((data) => {
      const handle = handleRef.current;
      if (handle) source.write(handle.session, data);
    });

    const observer =
      typeof ResizeObserver === "undefined"
        ? null
        : new ResizeObserver(() => {
            if (host.clientWidth === 0 || host.clientHeight === 0) return;
            fit.fit();
            const handle = handleRef.current;
            if (!handle) return;
            const next = clampGrid(terminal.cols, terminal.rows);
            source.resize(handle.session, next.cols, next.rows);
          });
    observer?.observe(host);

    return () => {
      disposed = true;
      for (const off of offs) off();
      offs = [];
      offInput.dispose();
      observer?.disconnect();
      const handle = handleRef.current;
      if (handle) source.kill(handle.session);
      handleRef.current = null;
      // Closing the tab must also release the machine's exclusive terminal
      // scope; without this the hub keeps it booked to this client until the
      // app's WebSocket drops, answering everyone else with "in use".
      source.release?.(runtimeId);
      terminal.dispose();
    };
    // The remote source and target are stable for a tab's lifetime; the tab
    // is keyed by runtime id, so this effect must run exactly once.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [runtimeId, source]);

  return (
    <div
      data-slot="floating-terminal-remote"
      data-runtime-id={runtimeId}
      className="min-h-0 flex-1 flex-col overflow-hidden bg-background"
      style={{ display: active ? "flex" : "none" }}
    >
      <div
        ref={hostRef}
        data-slot="floating-terminal-remote-body"
        className="min-h-0 flex-1 overflow-hidden"
      />
      {status.state === "connecting" && (
        <p
          data-slot="floating-terminal-remote-status"
          className="flex shrink-0 items-center gap-1.5 border-t border-surface-border px-2 py-1 text-[11px] text-muted-foreground"
        >
          <Loader2 aria-hidden className="size-3 animate-spin" />
          {t(($) => $.desktop.terminal.connecting, { machine: label })}
        </p>
      )}
      {status.state === "error" && (
        <p
          data-slot="floating-terminal-remote-status"
          className="shrink-0 border-t border-surface-border px-2 py-1 text-[11px] text-destructive"
        >
          {status.errorKey === "err_in_use"
            ? t(($) => $.desktop.terminal.err_in_use)
            : status.errorKey === "err_forbidden"
              ? t(($) => $.desktop.terminal.err_forbidden)
              : status.errorKey === "err_unsupported"
                ? t(($) => $.desktop.terminal.err_unsupported)
                : status.errorKey === "err_offline"
                  ? t(($) => $.desktop.terminal.err_offline)
                  : t(($) => $.desktop.terminal.err_generic)}
        </p>
      )}
      {status.state === "exited" && (
        <p
          data-slot="floating-terminal-remote-status"
          className="shrink-0 border-t border-surface-border px-2 py-1 text-[11px] text-muted-foreground"
        >
          {t(($) => $.desktop.terminal.exited, { code: status.code ?? 0 })}
        </p>
      )}
    </div>
  );
}

/**
 * Floating terminal panel: an xterm instance wired to a main-process pty,
 * toggled with Ctrl+` from anywhere and draggable/resizable inside the tab
 * canvas.
 *
 * State split (see the store): Zustand owns only the UI state — open/closed,
 * geometry, active session. Pty bytes never pass through it; `onData`/`onExit`
 * write into xterm directly.
 *
 * The panel is mounted for the shell's whole lifetime and only hidden when
 * closed, so toggling keeps both the scrollback and the shell process. It dies
 * with the window, which is also what kills the pty on main's side.
 */
/**
 * useWS that degrades to null outside a WSProvider (dedicated issue windows,
 * tests). The context-missing throw surfaces after `use` consumed the hook,
 * so hook order stays stable across renders.
 */
function useOptionalWS(): ReturnType<typeof useWS> | null {
  try {
    return useWS();
  } catch {
    return null;
  }
}

export function FloatingTerminal({
  remoteSource: injectedRemoteSource,
}: {
  /** Test injection point; defaults to the realtime-hub transport. */
  remoteSource?: TerminalSessionSource | null;
} = {}) {
  const resolvedTheme = useTheme().resolvedTheme;
  const visible = useTerminalStore((state) => state.visible);
  const rect = useTerminalStore((state) => state.rect);
  const activeSession = useTerminalStore((state) => state.activeSession);
  const setVisible = useTerminalStore((state) => state.setVisible);
  const setRect = useTerminalStore((state) => state.setRect);
  const machines = useTerminalMachines();

  // The realtime WS context is only present under CoreProvider (main window
  // with an authenticated session); a bare mount — dedicated issue windows,
  // tests — degrades remote tabs to unavailable rather than throwing.
  const ws = useOptionalWS();
  const remoteSource = useMemo<TerminalSessionSource | null>(() => {
    if (injectedRemoteSource !== undefined) return injectedRemoteSource;
    if (!ws) return null;
    const context = ws;
    return new RemoteSessionSource({
      subscribe: (type, handler) => context.subscribe(type as never, handler),
      send: (message) => context.send(message as never),
    });
  }, [injectedRemoteSource, ws]);

  // Active target ("local" or a runtime id) and the remote tabs already opened.
  const [target, setTarget] = useState<TerminalTarget>("local");
  const [remoteTabs, setRemoteTabs] = useState<TerminalMachine[]>([]);

  const handleTargetChange = useCallback(
    (next: TerminalTarget) => {
      setTarget(next);
      if (next === "local" || !remoteSource) return;
      setRemoteTabs((tabs) => {
        if (tabs.some((tab) => tab.id === next)) return tabs;
        const machine = machines.find((candidate) => candidate.id === next);
        return machine ? [...tabs, machine] : tabs;
      });
    },
    [machines, remoteSource],
  );

  const closeRemoteTab = useCallback((runtimeId: string) => {
    setRemoteTabs((tabs) => tabs.filter((tab) => tab.id !== runtimeId));
    setTarget((current) => (current === runtimeId ? "local" : current));
  }, []);

  const panelRef = useRef<HTMLDivElement | null>(null);
  const hostRef = useRef<HTMLDivElement | null>(null);
  const terminalRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const sessionRef = useRef<string | null>(null);
  /**
   * The preload bridge, captured on mount. Optional on purpose: an old preload
   * attached to a refreshed React tree (HMR, dev) exposes no terminalAPI, and
   * the panel must degrade to inert chrome instead of throwing on every
   * DesktopShell mount — the same tolerance `onOpenSettings` callers apply.
   */
  const bridgeRef = useRef<Window["terminalAPI"] | null>(null);
  const disposedRef = useRef(false);
  const [bounds, setBounds] = useState<TerminalCanvasBounds | null>(null);
  const [exitCode, setExitCode] = useState<number | null>(null);
  const [spawnFailed, setSpawnFailed] = useState(false);

  const resolved = useMemo(
    () => resolveTerminalRect(rect, bounds),
    [rect, bounds],
  );
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
  );

  /** Fit xterm to the host box and tell main the resulting grid. */
  const applyFit = useCallback(() => {
    const terminal = terminalRef.current;
    const fit = fitRef.current;
    const host = hostRef.current;
    if (!terminal || !fit || !host) return;
    // A zero-sized host (panel closed, window minimised) would propose a
    // degenerate grid; main rejects it, so skip the round-trip entirely.
    if (host.clientWidth === 0 || host.clientHeight === 0) return;
    fit.fit();
    const sessionId = sessionRef.current;
    const bridge = bridgeRef.current;
    if (!sessionId || !bridge) return;
    const grid = clampGrid(terminal.cols, terminal.rows);
    bridge.resize(sessionId, grid.cols, grid.rows);
  }, []);

  const spawnSession = useCallback(() => {
    const terminal = terminalRef.current;
    const bridge = bridgeRef.current;
    if (!terminal || !bridge || sessionRef.current) return;
    // Fit before spawning so the shell starts at the panel's real grid instead
    // of xterm's default 80x24 and reflowing on the first prompt.
    applyFit();
    const grid = clampGrid(terminal.cols, terminal.rows);
    void bridge.spawn(grid).then((result) => {
      if (disposedRef.current) {
        // The panel went away while main was spawning; the pty must not
        // outlive the WebContents that asked for it.
        if (result.ok) bridge.kill(result.sessionId);
        return;
      }
      if (!result.ok) {
        setSpawnFailed(true);
        return;
      }
      sessionRef.current = result.sessionId;
      setSpawnFailed(false);
      setExitCode(null);
      useTerminalStore.getState().setActiveSession(result.sessionId);
      terminal.focus();
    });
  }, [applyFit]);

  // xterm lives as long as the panel does: created once, disposed on unmount.
  useEffect(() => {
    disposedRef.current = false;
    const host = hostRef.current;
    const bridge = window.terminalAPI ?? null;
    bridgeRef.current = bridge;
    if (!host || !bridge) return undefined;
    const terminal = new Terminal({
      fontSize: TERMINAL_FONT_SIZE,
      fontFamily: readToken(host, TERMINAL_FONT_TOKEN) || undefined,
      scrollback: TERMINAL_SCROLLBACK,
      cursorBlink: true,
      theme: readTerminalTheme(host),
    });
    const fit = new FitAddon();
    terminal.loadAddon(fit);
    terminal.open(host);
    terminalRef.current = terminal;
    fitRef.current = fit;

    // Pty output goes straight into xterm — never through the store.
    const offData = bridge.onData((event) => {
      if (event.sessionId !== sessionRef.current) return;
      terminal.write(event.data);
    });
    const offExit = bridge.onExit((event) => {
      if (event.sessionId !== sessionRef.current) return;
      // Drop the session before the state update so a restart cannot write to
      // the dead pty, and clear it in the store so the UI stops claiming a
      // live shell.
      sessionRef.current = null;
      useTerminalStore.getState().setActiveSession(null);
      setExitCode(event.exitCode);
    });
    const offInput = terminal.onData((data) => {
      const sessionId = sessionRef.current;
      if (!sessionId) return;
      bridge.write(sessionId, data);
    });

    return () => {
      disposedRef.current = true;
      offData();
      offExit();
      offInput.dispose();
      const sessionId = sessionRef.current;
      if (sessionId) bridge.kill(sessionId);
      sessionRef.current = null;
      bridgeRef.current = null;
      useTerminalStore.getState().setActiveSession(null);
      terminal.dispose();
      terminalRef.current = null;
      fitRef.current = null;
    };
  }, []);

  // Re-read the palette whenever the app theme resolves to a different value;
  // the tokens themselves carry the light/dark split.
  useEffect(() => {
    const terminal = terminalRef.current;
    const host = hostRef.current;
    if (!terminal || !host) return;
    terminal.options.theme = readTerminalTheme(host);
  }, [resolvedTheme]);

  // Resizing the panel (or the window) must propagate a new grid to the pty,
  // otherwise the shell keeps formatting for the old width.
  useEffect(() => {
    const host = hostRef.current;
    if (!host || typeof ResizeObserver === "undefined") return undefined;
    const observer = new ResizeObserver(() => applyFit());
    observer.observe(host);
    return () => observer.disconnect();
  }, [applyFit]);

  // Geometry is clamped against the canvas, so its size is tracked too: a
  // nested shrink of the window re-clamps a panel that would otherwise hang
  // off the edge.
  useEffect(() => {
    const canvas = panelRef.current?.parentElement;
    if (!canvas || typeof ResizeObserver === "undefined") return undefined;
    const measure = () =>
      setBounds({ width: canvas.clientWidth, height: canvas.clientHeight });
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(canvas);
    return () => observer.disconnect();
  }, []);

  // Opening the panel starts a shell if there is none. A shell that exited (or
  // failed to start) waits for an explicit restart — auto-respawning would spin
  // on a broken environment. Only the local tab owns this lifecycle.
  useEffect(() => {
    if (target !== "local") return;
    if (!visible || activeSession || sessionRef.current) return;
    if (exitCode !== null || spawnFailed) return;
    spawnSession();
  }, [target, visible, activeSession, exitCode, spawnFailed, spawnSession]);

  const handleDragEnd = (event: DragEndEvent) => {
    setRect(
      resolveTerminalRect(
        {
          x: resolved.x + Math.round(event.delta.x),
          y: resolved.y + Math.round(event.delta.y),
          width: resolved.width,
          height: resolved.height,
        },
        bounds,
      ),
    );
  };

  const handleResizeStart = (
    event: ReactPointerEvent<HTMLDivElement>,
    direction: ResizeDirection,
  ) => {
    event.preventDefault();
    // The handles sit inside the panel; without this the pointer sensor would
    // read the gesture as a drag of the whole panel.
    event.stopPropagation();
    const startX = event.clientX;
    const startY = event.clientY;
    const start = resolved;
    const handle = event.currentTarget;
    handle.setPointerCapture?.(event.pointerId);

    const onMove = (moveEvent: PointerEvent) => {
      const width =
        direction === "bottom" ? start.width : start.width + (moveEvent.clientX - startX);
      const height =
        direction === "right" ? start.height : start.height + (moveEvent.clientY - startY);
      setRect(
        resolveTerminalRect(
          { x: start.x, y: start.y, width, height },
          bounds,
        ),
      );
    };
    const onEnd = () => {
      handle.removeEventListener("pointermove", onMove);
      handle.removeEventListener("pointerup", onEnd);
      handle.removeEventListener("pointercancel", onEnd);
    };
    handle.addEventListener("pointermove", onMove);
    handle.addEventListener("pointerup", onEnd);
    handle.addEventListener("pointercancel", onEnd);
  };

  return (
    <DndContext sensors={sensors} onDragEnd={handleDragEnd}>
      <TerminalPanel
        canRestart={!activeSession && (exitCode !== null || spawnFailed)}
        exitCode={exitCode}
        hostRef={hostRef}
        localHidden={target !== "local"}
        machines={machines}
        onResizeStart={handleResizeStart}
        onTargetChange={handleTargetChange}
        target={target}
        onHide={() => setVisible(false)}
        onRestart={() => {
          // Clearing both is what re-arms the spawn effect; spawning here as
          // well would start a second pty.
          setExitCode(null);
          setSpawnFailed(false);
        }}
        panelRef={panelRef}
        resolved={resolved}
        spawnFailed={spawnFailed}
        visible={visible}
      >
        {remoteTabs.map((tab) => (
          <RemoteTerminalView
            active={target === tab.id}
            key={tab.id}
            label={tab.label}
            onDaemonOffline={closeRemoteTab}
            runtimeId={tab.id}
            source={remoteSource as TerminalSessionSource}
          />
        ))}
      </TerminalPanel>
    </DndContext>
  );
}
