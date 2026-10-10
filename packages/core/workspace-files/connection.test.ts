// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { WSClient } from "../api/ws-client";
import { requestWorkspaceFiles } from "./client";
import type { WSMessage } from "../types/events";

class Socket {
  static OPEN = 1;
  static current: Socket;
  readyState = 0;
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  frames: WSMessage[] = [];
  constructor() { Socket.current = this; }
  send(raw: string) { this.frames.push(JSON.parse(raw)); }
  close() { this.readyState = 3; }
}

afterEach(() => vi.unstubAllGlobals());

describe("Files first connection through the real WS client", () => {
  it.each([false, true])("recovers an unsent resource request after authentication (cookieAuth=%s)", async (cookieAuth) => {
    vi.stubGlobal("WebSocket", Socket);
    const ws = new WSClient("ws://example.test/ws", { cookieAuth });
    ws.setAuth(cookieAuth ? null : "synthetic-token", "workspace");
    const transport = { send: ws.send.bind(ws), subscribe: ws.on.bind(ws) };
    const request = { operation: "resources", context: { kind: "project", project_id: "project" } } as const;
    ws.connect();
    let recovered: ReturnType<typeof requestWorkspaceFiles> | undefined;
    const reconnect = vi.fn();
    ws.onReconnect(reconnect);
    ws.onReady(() => { recovered = requestWorkspaceFiles(transport, request); });
    await expect(requestWorkspaceFiles(transport, request)).rejects.toMatchObject({ code: "unavailable" });
    const socket = Socket.current;
    expect(socket.frames).toEqual([]);
    socket.readyState = Socket.OPEN;
    socket.onopen!();
    if (!cookieAuth) {
      expect(recovered).toBeUndefined();
      expect(socket.frames.map(frame => frame.type)).toEqual(["auth"]);
      socket.onmessage!({ data: JSON.stringify({ type: "auth_ack" }) });
    }
    expect(reconnect).not.toHaveBeenCalled();
    const frames = socket.frames.filter(frame => frame.type === "workspace_files.resources");
    expect(frames).toHaveLength(1);
    socket.onmessage!({ data: JSON.stringify({ type: "workspace_files.resources_result", payload: {
      client_req_id: (frames[0]!.payload as { client_req_id: string }).client_req_id,
      context: request.context,
      resources: [{ resource_id: "resource", display_name: "test", access: "read_only" }],
    } }) });
    await expect(recovered).resolves.toMatchObject({ resources: [{ resource_id: "resource" }] });
    ws.disconnect();
  });
});
