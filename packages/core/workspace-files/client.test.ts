// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import { requestWorkspaceFiles } from "./client";

function transport() {
  const handlers = new Map<string, (payload: unknown) => void>();
  return {
    send: vi.fn(),
    subscribe: vi.fn((event: string, handler: (payload: unknown) => void) => {
      handlers.set(event, handler);
      return () => handlers.delete(event);
    }),
    handlers,
  };
}
const context = { kind: "project", project_id: "00000000-0000-4000-8000-000000000001" } as const;

describe("workspace files client", () => {
  it("scopes results by request and context and strips unknown resource fields", async () => {
    const ws = transport();
    const pending = requestWorkspaceFiles(ws, { operation: "resources", context });
    const { client_req_id } = ws.send.mock.calls[0]![0].payload;
    ws.handlers.get("workspace_files.resources_result")?.({ client_req_id: "other", context, resources: [] });
    expect(ws.handlers.size).toBeGreaterThan(0);
    ws.handlers.get("workspace_files.resources_result")?.({ client_req_id, context, resources: [{ resource_id: "r", display_name: "Directory", access: "read_only", local_path: "private" }] });
    expect(await pending).toEqual({ resources: [{ resource_id: "r", display_name: "Directory", access: "read_only" }] });
    expect(ws.handlers.size).toBe(0);
  });
  it("cancels on close and ignores late responses", async () => {
    const ws = transport();
    const controller = new AbortController();
    const pending = requestWorkspaceFiles(ws, { operation: "list", context, resourceId: "r", path: "." }, controller.signal);
    const { client_req_id } = ws.send.mock.calls[0]![0].payload;
    controller.abort();
    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
    expect(ws.send).toHaveBeenLastCalledWith({ type: "workspace_files.cancel", payload: { client_req_id } });
    expect(ws.handlers.size).toBe(0);
  });
  it("refuses malformed and mismatched replies instead of reporting empty resources", async () => {
    const ws = transport();
    const pending = requestWorkspaceFiles(ws, { operation: "resources", context });
    const { client_req_id } = ws.send.mock.calls[0]![0].payload;
    ws.handlers.get("workspace_files.resources_result")?.({ client_req_id, context: { kind: "issue", issue_id: "wrong" }, resources: [] });
    await expect(pending).rejects.toMatchObject({ code: "unavailable" });
  });
  it("maps server errors without exposing raw payloads", async () => {
    const ws = transport();
    const pending = requestWorkspaceFiles(ws, { operation: "resources", context });
    const { client_req_id } = ws.send.mock.calls[0]![0].payload;
    ws.handlers.get("workspace_files.error")?.({ client_req_id, code: "forbidden", root_path: "private" });
    await expect(pending).rejects.toMatchObject({ code: "forbidden" });
  });
  it("rejects malformed resources rather than treating them as empty", async () => {
    const ws = transport();
    const pending = requestWorkspaceFiles(ws, { operation: "resources", context });
    const { client_req_id } = ws.send.mock.calls[0]![0].payload;
    ws.handlers.get("workspace_files.resources_result")?.({ client_req_id, context, resources: [{ local_path: "private" }] });
    await expect(pending).rejects.toMatchObject({ code: "unavailable" });
  });
  it("requires ordered list frames from the selected resource", async () => {
    const ws = transport();
    const pending = requestWorkspaceFiles(ws, { operation: "list", context, resourceId: "r", path: "." });
    const { client_req_id } = ws.send.mock.calls[0]![0].payload;
    ws.handlers.get("workspace_files.list_result")?.({ client_req_id, resource_id: "other", seq: 0, entries: [], final: true });
    await expect(pending).rejects.toMatchObject({ code: "unavailable" });
  });
  it("aggregates a bounded UTF-8 read without retaining the subscriptions", async () => {
    const ws = transport();
    const pending = requestWorkspaceFiles(ws, { operation: "read", context, resourceId: "r", path: "fixture.txt" });
    const { client_req_id } = ws.send.mock.calls[0]![0].payload;
    ws.handlers.get("workspace_files.read_chunk")?.({ client_req_id, resource_id: "r", seq: 0, data: btoa("fixture"), eof: true });
    expect(await pending).toEqual({ text: "fixture" });
    expect(ws.handlers.size).toBe(0);
  });
  it("timeouts cancel the pending request", async () => {
    vi.useFakeTimers();
    try {
      const ws = transport();
      const pending = requestWorkspaceFiles(ws, { operation: "resources", context });
      const rejected = expect(pending).rejects.toMatchObject({ code: "timeout" });
      await vi.advanceTimersByTimeAsync(10_000);
      await rejected;
      expect(ws.send).toHaveBeenLastCalledWith(expect.objectContaining({ type: "workspace_files.cancel" }));
      expect(ws.handlers.size).toBe(0);
    } finally { vi.useRealTimers(); }
  });
});
