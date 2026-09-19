import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { createPersistStorage, defaultStorage } from "@multica/core/platform";

/**
 * UI state for the floating terminal panel: whether it is open, where it sits,
 * and which pty session backs it.
 *
 * Deliberately UI-only. Pty output is a stream, not state — it goes straight
 * from `window.terminalAPI` into the xterm instance inside the panel, never
 * through Zustand (see the state rules in AGENTS.md).
 *
 * `activeSession` is the sessionId main minted for the open panel. It is NOT
 * persisted: pty sessions live inside the app instance, so a restored id would
 * only be a pointer to a process that no longer exists.
 */
export interface TerminalRect {
  /**
   * `null` means "no position saved yet". The panel then anchors itself to the
   * canvas's bottom-left, the same "let the platform decide" encoding
   * window-state.ts uses for a window with no saved bounds — a fresh install
   * must not drop the terminal onto the page header.
   */
  x: number | null;
  y: number | null;
  width: number;
  height: number;
}

/** A rect with concrete coordinates, ready to hand to CSS. */
export interface ResolvedTerminalRect {
  x: number;
  y: number;
  width: number;
  height: number;
}

export interface TerminalCanvasBounds {
  width: number;
  height: number;
}

export const TERMINAL_MIN_WIDTH = 320;
export const TERMINAL_MIN_HEIGHT = 180;
export const TERMINAL_DEFAULT_WIDTH = 720;
export const TERMINAL_DEFAULT_HEIGHT = 360;
/** Gap kept between the panel and the canvas edges. */
export const TERMINAL_EDGE_INSET = 16;

export function defaultTerminalRect(): TerminalRect {
  return {
    x: null,
    y: null,
    width: TERMINAL_DEFAULT_WIDTH,
    height: TERMINAL_DEFAULT_HEIGHT,
  };
}

function parseCoordinate(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value)
    ? Math.round(value)
    : null;
}

/**
 * Parse a persisted rect as untrusted input. Returns null when the payload is
 * unusable so the caller can fall back to the default — a corrupt entry never
 * blocks the panel, the same contract as window-state.ts's parseWindowState.
 */
export function parseTerminalRect(value: unknown): TerminalRect | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const obj = value as Record<string, unknown>;
  const width = parseCoordinate(obj.width);
  const height = parseCoordinate(obj.height);
  if (width === null || height === null) return null;
  return {
    x: parseCoordinate(obj.x),
    y: parseCoordinate(obj.y),
    width: Math.max(width, TERMINAL_MIN_WIDTH),
    height: Math.max(height, TERMINAL_MIN_HEIGHT),
  };
}

function clamp(value: number, min: number, max: number): number {
  return Math.max(min, Math.min(max, value));
}

/**
 * Fold a saved (or freshly dragged) rect into concrete, on-canvas coordinates.
 *
 * Mirrors resolveWindowOptions in window-state.ts: geometry is clamped against
 * the live container so a rect saved on a larger window — or a display that is
 * gone — can never restore the panel off-screen or larger than its canvas.
 */
export function resolveTerminalRect(
  rect: TerminalRect,
  bounds: TerminalCanvasBounds | null,
): ResolvedTerminalRect {
  const width = Math.max(rect.width, TERMINAL_MIN_WIDTH);
  const height = Math.max(rect.height, TERMINAL_MIN_HEIGHT);

  // No measurable canvas yet (first paint, collapsed window, jsdom): keep the
  // requested size and fall back to the inset origin. Clamping against a
  // zero-sized box here would collapse the panel to nothing.
  const usable = bounds && bounds.width > 0 && bounds.height > 0 ? bounds : null;
  if (!usable) {
    return {
      x: rect.x ?? TERMINAL_EDGE_INSET,
      y: rect.y ?? TERMINAL_EDGE_INSET,
      width,
      height,
    };
  }

  const resolvedWidth = Math.min(width, usable.width);
  const resolvedHeight = Math.min(height, usable.height);
  const maxX = Math.max(usable.width - resolvedWidth, 0);
  const maxY = Math.max(usable.height - resolvedHeight, 0);
  return {
    // Unpositioned panels sit bottom-left: clear of the chat launcher in the
    // opposite corner, and out of the way of the page content above.
    x: rect.x === null ? Math.min(TERMINAL_EDGE_INSET, maxX) : clamp(rect.x, 0, maxX),
    y: rect.y === null ? Math.max(maxY - TERMINAL_EDGE_INSET, 0) : clamp(rect.y, 0, maxY),
    width: resolvedWidth,
    height: resolvedHeight,
  };
}

interface TerminalStore {
  visible: boolean;
  rect: TerminalRect;
  activeSession: string | null;
  toggle: () => void;
  setVisible: (visible: boolean) => void;
  setRect: (rect: TerminalRect) => void;
  setActiveSession: (sessionId: string | null) => void;
}

export const useTerminalStore = create<TerminalStore>()(
  persist(
    (set) => ({
      visible: false,
      rect: defaultTerminalRect(),
      activeSession: null,
      toggle: () => set((state) => ({ visible: !state.visible })),
      setVisible: (visible) => set({ visible }),
      setRect: (rect) => set({ rect }),
      setActiveSession: (activeSession) => set({ activeSession }),
    }),
    {
      name: "multica_floating_terminal",
      version: 1,
      storage: createJSONStorage(() => createPersistStorage(defaultStorage)),
      // Open/closed and geometry survive a restart; the session id does not.
      partialize: (state) => ({ visible: state.visible, rect: state.rect }),
      merge: (persistedState, currentState) => {
        const persisted = persistedState as
          | Partial<{ visible: unknown; rect: unknown }>
          | undefined;
        return {
          ...currentState,
          visible: persisted?.visible === true,
          rect: parseTerminalRect(persisted?.rect) ?? defaultTerminalRect(),
        };
      },
    },
  ),
);
