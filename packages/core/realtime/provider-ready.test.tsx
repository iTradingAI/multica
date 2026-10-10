// @vitest-environment jsdom
import { useEffect } from "react";
import { act, cleanup, render } from "@testing-library/react";
import { create } from "zustand";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AuthState } from "../auth/store";
import { WSProvider, useWS } from "./provider";

vi.mock("./use-realtime-sync", () => ({ useRealtimeSync: () => {} }));
vi.mock("../search-index/hooks", () => ({ useLocalSearchIndexSync: () => {} }));
vi.mock("../platform/workspace-storage", () => ({
  getCurrentSlug: () => "test-workspace", getCurrentWsId: () => "test-workspace-id",
  subscribeToCurrentSlug: () => () => {},
}));

class Socket {
  static OPEN = 1;
  static current: Socket;
  readyState = 0;
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  frames: string[] = [];
  constructor() { Socket.current = this; }
  send(raw: string) { this.frames.push(raw); }
  close() { this.readyState = 3; }
}

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

describe("WS provider authenticated readiness", () => {
  it.each([false, true])("wires first readiness and send status to consumers (cookieAuth=%s)", (cookieAuth) => {
    vi.stubGlobal("WebSocket", Socket);
    const authStore = create<AuthState>(() => ({ user: { id: "test-user" } }) as AuthState);
    const ready = vi.fn();
    let current: ReturnType<typeof useWS>;
    function Consumer() {
      const ws = useWS();
      const { onReady } = ws;
      current = ws;
      useEffect(() => onReady(ready), [onReady]);
      return null;
    }
    const view = render(<WSProvider wsUrl="ws://example.test/ws" authStore={authStore}
      storage={{ getItem: () => "synthetic-token", setItem: () => {}, removeItem: () => {} }} cookieAuth={cookieAuth}>
      <Consumer />
    </WSProvider>);
    const frame = { type: "workspace_files.resources", payload: {} } as const;
    expect(current!.send(frame)).toBe(false);
    const socket = Socket.current;
    act(() => { socket.readyState = Socket.OPEN; socket.onopen!(); });
    if (!cookieAuth) {
      expect(ready).not.toHaveBeenCalled();
      expect(current!.send(frame)).toBe(false);
      act(() => socket.onmessage!({ data: JSON.stringify({ type: "auth_ack" }) }));
    }
    expect(ready).toHaveBeenCalledTimes(1);
    expect(current!.send(frame)).toBe(true);
    const retiredMessage = socket.onmessage!;
    view.unmount();
    retiredMessage({ data: JSON.stringify({ type: "auth_ack" }) });
    expect(ready).toHaveBeenCalledTimes(1);
    expect(socket.onmessage).toBeNull();
  });
});
