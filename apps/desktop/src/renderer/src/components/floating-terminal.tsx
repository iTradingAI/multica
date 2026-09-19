import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type PointerEvent as ReactPointerEvent,
  type RefObject,
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
import { RotateCcw, SquareTerminal, X } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { useTheme } from "@multica/ui/components/common/theme-provider";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "@multica/views/i18n";
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
  onHide: () => void;
  onRestart: () => void;
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
  onHide,
  onRestart,
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
          set on the Terminal instance is what paints. */}
      <div
        ref={hostRef}
        data-slot="floating-terminal-body"
        className="min-h-0 flex-1 overflow-hidden bg-background"
      />

      {(exitCode !== null || spawnFailed) && (
        <p
          data-slot="floating-terminal-status"
          className="shrink-0 border-t border-surface-border px-2 py-1 text-[11px] text-muted-foreground"
        >
          {spawnFailed
            ? t(($) => $.desktop.terminal.spawn_failed)
            : t(($) => $.desktop.terminal.exited, { code: exitCode })}
        </p>
      )}

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
export function FloatingTerminal() {
  const resolvedTheme = useTheme().resolvedTheme;
  const visible = useTerminalStore((state) => state.visible);
  const rect = useTerminalStore((state) => state.rect);
  const activeSession = useTerminalStore((state) => state.activeSession);
  const setVisible = useTerminalStore((state) => state.setVisible);
  const setRect = useTerminalStore((state) => state.setRect);

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
  // on a broken environment.
  useEffect(() => {
    if (!visible || activeSession || sessionRef.current) return;
    if (exitCode !== null || spawnFailed) return;
    spawnSession();
  }, [visible, activeSession, exitCode, spawnFailed, spawnSession]);

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
        onHide={() => setVisible(false)}
        onResizeStart={handleResizeStart}
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
      />
    </DndContext>
  );
}
