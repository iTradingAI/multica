import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../test/i18n";
import { FilesTab } from "./files-tab";

const state = vi.hoisted(() => ({
  handlers: new Map<string, Set<(payload: unknown) => void>>(),
  send: vi.fn(),
  reconnect: () => {},
  holdRead: false,
  badCursor: false,
}));
function emit(type: string, payload: unknown) {
  state.handlers.get(type)?.forEach((handler) => handler(payload));
}
function subscribe(type: string, handler: (payload: unknown) => void) {
  const entries = state.handlers.get(type) ?? new Set();
  entries.add(handler);
  state.handlers.set(type, entries);
  return () => {
    entries.delete(handler);
  };
}
const onReconnect = (callback: () => void) => {
  state.reconnect = callback;
  return () => {};
};
vi.mock("@multica/core/realtime", () => ({
  useWS: () => ({ send: state.send, subscribe, onReconnect }),
}));
vi.mock("@multica/core/config", () => ({ useFeatureEnabled: () => false }));
vi.mock("../platform/local-directory", () => ({ isDesktopShell: () => false }));

beforeEach(() => {
  state.handlers.clear();
  state.send.mockReset();
  state.holdRead = false;
  state.badCursor = false;
  state.send.mockImplementation(
    ({ type, payload }: { type: string; payload: Record<string, unknown> }) =>
      queueMicrotask(() => {
        if (type === "workspace_files.resources")
          emit("workspace_files.resources_result", {
            client_req_id: payload.client_req_id,
            context: payload.context,
            resources: [
              {
                resource_id: "resource",
                display_name: "fixture",
                access: "read_only",
              },
            ],
          });
        if (type === "workspace_files.list") {
          if (payload.cursor && state.badCursor) {
            emit("workspace_files.error", {
              client_req_id: payload.client_req_id,
              code: "invalid_cursor",
            });
            return;
          }
          const entries =
            payload.path === "."
              ? payload.cursor
                ? [{ name: "second.txt", type: "regular" }]
                : [
                    { name: "nested", type: "directory" },
                    { name: "note.txt", type: "regular" },
                  ]
              : [{ name: "child.txt", type: "regular" }];
          emit("workspace_files.list_result", {
            client_req_id: payload.client_req_id,
            resource_id: "resource",
            seq: 0,
            entries,
            next_cursor: payload.path === "." && !payload.cursor ? "next" : "",
            final: true,
          });
        }
        if (type === "workspace_files.read" && !state.holdRead)
          emit("workspace_files.read_chunk", {
            client_req_id: payload.client_req_id,
            resource_id: "resource",
            seq: 0,
            data: btoa("selected evidence"),
            eof: true,
          });
      }),
  );
});
function setup() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return renderWithI18n(
    <QueryClientProvider client={client}>
      <FilesTab
        wsId="workspace"
        context={{ kind: "project", project_id: "project" }}
        hasProject
      />
    </QueryClientProvider>,
  );
}
const requests = (type: string) =>
  state.send.mock.calls
    .map(([frame]) => frame)
    .filter((frame) => frame.type === type);

describe("Files tree lifecycle (renderer A only)", () => {
  it("loads only expanded nodes and pages with the returned cursor", async () => {
    setup();
    await screen.findByRole("button", { name: "note.txt" });
    expect(
      requests("workspace_files.list").map((frame) => frame.payload.path),
    ).toEqual(["."]);
    fireEvent.click(screen.getByRole("button", { name: "Expand nested" }));
    await screen.findByRole("button", { name: "child.txt" });
    expect(
      requests("workspace_files.list").map((frame) => frame.payload.path),
    ).toEqual([".", "nested"]);
    fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    await screen.findByRole("button", { name: "second.txt" });
    expect(requests("workspace_files.list").at(-1).payload.cursor).toBe("next");
    fireEvent.click(screen.getByRole("button", { name: "Collapse nested" }));
    expect(
      screen.queryByRole("button", { name: "child.txt" }),
    ).not.toBeInTheDocument();
  });
  it("invalid cursor discards old pages and restarts at the first page", async () => {
    setup();
    await screen.findByRole("button", { name: "note.txt" });
    state.badCursor = true;
    fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    await screen.findByRole("button", { name: "Retry" });
    expect(
      screen.queryByRole("button", { name: "note.txt" }),
    ).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await screen.findByRole("button", { name: "note.txt" });
    expect(
      requests("workspace_files.list").at(-1).payload.cursor,
    ).toBeUndefined();
  });
  it("binding event clears selection and rejects late read frames", async () => {
    setup();
    await screen.findByRole("button", { name: "note.txt" });
    state.holdRead = true;
    fireEvent.click(screen.getByRole("button", { name: "note.txt" }));
    await waitFor(() =>
      expect(requests("workspace_files.read")).toHaveLength(1),
    );
    const id = requests("workspace_files.read")[0].payload.client_req_id;
    act(() => emit("project_resource:updated", {}));
    await screen.findByRole("button", { name: "note.txt" });
    expect(
      requests("workspace_files.cancel").some(
        (frame) => frame.payload.client_req_id === id,
      ),
    ).toBe(true);
    act(() =>
      emit("workspace_files.read_chunk", {
        client_req_id: id,
        resource_id: "resource",
        seq: 0,
        data: btoa("late private content"),
        eof: true,
      }),
    );
    expect(screen.queryByText("late private content")).not.toBeInTheDocument();
  });
  it("refresh retires an active preview instead of reusing its buffer", async () => {
    setup();
    await screen.findByRole("button", { name: "note.txt" });
    state.holdRead = true;
    fireEvent.click(screen.getByRole("button", { name: "note.txt" }));
    await waitFor(() =>
      expect(requests("workspace_files.read")).toHaveLength(1),
    );
    const id = requests("workspace_files.read")[0].payload.client_req_id;
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    await screen.findByRole("button", { name: "note.txt" });
    expect(
      requests("workspace_files.cancel").some(
        (frame) => frame.payload.client_req_id === id,
      ),
    ).toBe(true);
    expect(requests("workspace_files.read")).toHaveLength(1);
  });
});
