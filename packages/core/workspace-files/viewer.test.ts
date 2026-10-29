// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import type { PluginInstallation } from "../types/plugin";
import { fileExtension, fileViewerMatch } from "./viewer-match";
import { requestWorkspaceFiles } from "./client";
import { selectFileViewer } from "./viewer-client";

function installation(
  id: string,
  grant = true,
  platforms: ("web" | "desktop")[] = [],
): PluginInstallation {
  return {
    id,
    enabled: true,
    granted_scopes: grant ? ["files:read"] : [],
    surfaces: [
      {
        key: "viewer",
        type: "file_viewer",
        extensions: ["md", "local"],
        platforms,
      },
    ],
  } as PluginInstallation;
}
describe("file viewer matching", () => {
  it("counts enabled platform matches before grants", () => {
    const one = installation("one");
    expect(fileViewerMatch([one], "note.MD", "web")?.installation.id).toBe(
      "one",
    );
    expect(
      fileViewerMatch(
        [one, installation("ungranted", false)],
        "note.md",
        "web",
      ),
    ).toBeNull();
    expect(
      fileViewerMatch(
        [one, installation("desktop", false, ["desktop"])],
        "note.md",
        "web",
      )?.installation.id,
    ).toBe("one");
    expect(
      fileViewerMatch([installation("ungranted", false)], "note.md", "web"),
    ).toBeNull();
    expect(
      fileViewerMatch([{ ...one, enabled: false }], "note.md", "web"),
    ).toBeNull();
    expect(fileViewerMatch([], "note.md", "web")).toBeNull();
  });
  it("uses final ASCII suffix and excludes plain dotfiles", () => {
    for (const name of [".env", "code", "readme.", "a.ｍｄ"])
      expect(fileExtension(name)).toBe("");
    expect(fileExtension(".env.local")).toBe("local");
    expect(fileExtension("folder.md/note.MD")).toBe("md");
  });
});

describe("host viewer selection lifecycle", () => {
  it("sends only selection identity on reads and cancels on abort", async () => {
    const handlers = new Map<string, (payload: unknown) => void>();
    const ws = {
      send: vi.fn(),
      subscribe: (event: string, handler: (payload: unknown) => void) => {
        handlers.set(event, handler);
        return () => handlers.delete(event);
      },
    };
    const controller = new AbortController();
    const request = {
      context: { kind: "issue", issue_id: "issue" } as const,
      resource_id: "resource",
      path: "private/note.md",
      installation_id: "installation",
      version_id: "version",
      surface_key: "viewer",
      digest: "digest",
      platform: "web" as const,
      mount_id: "mount",
      generation: 1,
    };
    const pending = selectFileViewer(ws, request, controller.signal, vi.fn());
    const selection = crypto.randomUUID();
    const id = ws.send.mock.calls[0]![0].payload.client_req_id;
    handlers.get("workspace_files.viewer_select_result")?.({
      client_req_id: id,
      selection_id: selection,
    });
    expect(await pending).toBe(selection);
    const read = requestWorkspaceFiles(
      ws,
      {
        operation: "viewer_read",
        context: request.context,
        resourceId: request.resource_id,
        path: request.path,
        selectionId: selection,
      },
      controller.signal,
    );
    expect(ws.send.mock.calls[1]![0].payload).toEqual({
      client_req_id: expect.any(String),
      selection_id: selection,
    });
    const rejected = expect(read).rejects.toMatchObject({ name: "AbortError" });
    controller.abort();
    await rejected;
    expect(handlers.size).toBe(0);
    expect(
      ws.send.mock.calls.filter(
        ([frame]) => frame.type === "workspace_files.cancel",
      ),
    ).toHaveLength(2);
  });
});
