// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import { requestWorkspaceFiles } from "./client";

const context = { kind: "issue", issue_id: "issue" } as const;
function transport() {
  const handlers = new Map<string, (payload: unknown) => void>();
  return {
    send: vi.fn(),
    subscribe: (event: string, handler: (payload: unknown) => void) => {
      handlers.set(event, handler);
      return () => handlers.delete(event);
    },
    handlers,
  };
}
describe("FilesPaginationAndPreviewBoundaries", () => {
  it("sends page and cursor and preserves the next cursor", async () => {
    const ws = transport();
    const pending = requestWorkspaceFiles(ws, {
      operation: "list",
      context,
      resourceId: "r",
      path: ".",
      pageSize: 200,
      cursor: "current",
    });
    const { client_req_id } = ws.send.mock.calls[0]![0].payload;
    expect(ws.send.mock.calls[0]![0].payload).toMatchObject({
      cursor: "current",
      page_size: 200,
    });
    ws.handlers.get("workspace_files.list_result")?.({
      client_req_id,
      resource_id: "r",
      seq: 0,
      entries: [],
      next_cursor: "next",
      skipped: 3,
      final: true,
    });
    expect(await pending).toEqual({
      entries: [],
      nextCursor: "next",
      skipped: 3,
    });
  });
  for (const text of ["\0", "a\u0001b", "\u001f"])
    it("blocks binary controls without exposing content", async () => {
      const ws = transport();
      const pending = requestWorkspaceFiles(ws, {
        operation: "read",
        context,
        resourceId: "r",
        path: "data",
      });
      const { client_req_id } = ws.send.mock.calls[0]![0].payload;
      ws.handlers.get("workspace_files.read_chunk")?.({
        client_req_id,
        resource_id: "r",
        seq: 0,
        data: btoa(text),
        eof: true,
      });
      await expect(pending).rejects.toMatchObject({ code: "binary_content" });
      expect(ws.handlers.size).toBe(0);
    });
  it("accepts empty text and exactly 1 MiB, rejects an extra byte", async () => {
    for (const size of [0, 1024 * 1024, 1024 * 1024 + 1]) {
      const ws = transport();
      const pending = requestWorkspaceFiles(ws, {
        operation: "read",
        context,
        resourceId: "r",
        path: "data",
      });
      const assertion =
        size > 1024 * 1024
          ? expect(pending).rejects.toMatchObject({ code: "too_large" })
          : pending;
      const { client_req_id } = ws.send.mock.calls[0]![0].payload;
      let remaining = size;
      let seq = 0;
      do {
        const length = Math.min(remaining, 32 * 1024);
        remaining -= length;
        ws.handlers.get("workspace_files.read_chunk")?.({
          client_req_id,
          resource_id: "r",
          seq: seq++,
          data: btoa("x".repeat(length)),
          eof: remaining === 0,
        });
      } while (remaining > 0);
      const result = await assertion;
      if (size <= 1024 * 1024)
        expect(result).toEqual({ text: "x".repeat(size) });
    }
  });
  it("rejects invalid UTF-8 and malicious directory names", async () => {
    const ws = transport();
    const pending = requestWorkspaceFiles(ws, {
      operation: "read",
      context,
      resourceId: "r",
      path: "data",
    });
    const { client_req_id } = ws.send.mock.calls[0]![0].payload;
    ws.handlers.get("workspace_files.read_chunk")?.({
      client_req_id,
      resource_id: "r",
      seq: 0,
      data: btoa("\xff"),
      eof: true,
    });
    await expect(pending).rejects.toMatchObject({ code: "invalid_utf8" });
    const listing = requestWorkspaceFiles(ws, {
      operation: "list",
      context,
      resourceId: "r",
      path: ".",
    });
    const id = ws.send.mock.calls[2]![0].payload.client_req_id;
    ws.handlers.get("workspace_files.list_result")?.({
      client_req_id: id,
      resource_id: "r",
      seq: 0,
      entries: [{ name: "../secret", type: "regular" }],
      final: true,
    });
    await expect(listing).rejects.toMatchObject({ code: "unavailable" });
  });
});
