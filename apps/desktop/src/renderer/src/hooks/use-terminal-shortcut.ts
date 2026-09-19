import { useEffect } from "react";
import { useTerminalStore } from "@/stores/terminal-store";

/**
 * Ctrl+` toggles the floating terminal panel.
 *
 * Main owns the chord (see main/keyboard-shortcuts.ts) so it fires while focus
 * sits in any editor, input, menu, or dialog; the panel is part of the tabbed
 * window's shell, so only that window subscribes. An issue window that also
 * listened would mark the channel ready and drain the request into a renderer
 * with no panel to show.
 */
export function useTerminalShortcut(): void {
  useEffect(() => {
    if (window.desktopAPI.windowContext?.kind === "issue") return undefined;
    // Optional call keeps renderer HMR safe while an old preload is still
    // attached to a refreshed React tree, same as useOpenSettingsShortcut.
    return window.desktopAPI.onToggleTerminal?.(() => {
      useTerminalStore.getState().toggle();
    });
  }, []);
}
