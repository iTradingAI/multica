import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../test/i18n";
import { FilesTab } from "./files-tab";

const state = vi.hoisted(() => ({
  handlers: new Map<string, Set<(payload: unknown) => void>>(),
  send: vi.fn(),
  holdRead: false,
  badCursor: false,
  connected: true,
  ready: () => {},
  deepTree: false,
  holdSave: false,
  editText: "disk text",
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
const subscribeStatus = (callback: () => void) => {
  state.ready = callback;
  return () => { state.ready = () => {}; };
};
vi.mock("@multica/core/realtime", () => ({
  useWS: () => ({ send: state.send, subscribe, subscribeStatus, getStatus: () => state.connected ? "connected" : "reconnecting" }),
}));
vi.mock("@multica/core/projects", () => ({ projectResourcesOptions: () => ({ queryKey: ["labels"], queryFn: async () => [] }) }));
vi.mock("@multica/core/config", () => ({ useFeatureEnabled: () => false }));
vi.mock("../platform/local-directory", () => ({ isDesktopShell: () => false }));

beforeEach(() => {
  state.handlers.clear();
  state.send.mockReset();
  state.holdRead = false;
  state.badCursor = false;
  state.connected = true;
  state.ready = () => {};
  state.deepTree = false;
  state.holdSave = false;
  state.editText = "disk text";
  state.send.mockImplementation(
    ({ type, payload }: { type: string; payload: Record<string, unknown> }) => {
      if (!state.connected) return false;
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
              : state.deepTree && payload.path === "nested"
                ? [{ name: "level-two", type: "directory" }]
                : state.deepTree && payload.path === "nested/level-two"
                  ? [{ name: "level-three", type: "directory" }]
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
        if (type === "workspace_files.edit_open")
          emit("workspace_files.edit_result", {
            client_req_id: payload.client_req_id, resource_id: "resource",
            binding_generation: 1, version: "A".repeat(43), data: btoa(state.editText),
          });
        if (type === "workspace_files.save" && !state.holdSave)
          emit("workspace_files.error", { client_req_id: payload.client_req_id, code: "conflict" });
        if (type === "workspace_files.read" && !state.holdRead)
          emit("workspace_files.read_chunk", {
            client_req_id: payload.client_req_id,
            resource_id: "resource",
            seq: 0,
            data: btoa("selected evidence"),
            eof: true,
          });
      });
      return true;
    },
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
  it("expands three directory levels lazily and reads the full nested path", async () => {
    state.deepTree = true;
    setup();
    await screen.findByRole("button", { name: "note.txt" });
    fireEvent.click(screen.getByRole("button", { name: "nested" }));
    fireEvent.click(await screen.findByRole("button", { name: "level-two" }));
    fireEvent.click(await screen.findByRole("button", { name: "level-three" }));
    expect(requests("workspace_files.list").map((frame) => frame.payload.path)).toEqual([
      ".", "nested", "nested/level-two", "nested/level-two/level-three",
    ]);
    fireEvent.doubleClick(await screen.findByRole("button", { name: "child.txt" }));
    await screen.findByText("selected evidence");
    expect(requests("workspace_files.read").at(-1).payload.path).toBe("nested/level-two/level-three/child.txt");
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    fireEvent.click(await screen.findByRole("button", { name: "nested" }));
    expect(screen.queryByRole("button", { name: "child.txt" })).not.toBeInTheDocument();
  });
  it("waits for the first authenticated connection without a speculative request", async () => {
    state.connected = false;
    setup();
    expect(requests("workspace_files.resources")).toHaveLength(0);
    act(() => {
      state.connected = true;
      state.ready();
    });
    await screen.findByRole("button", { name: "note.txt" });
    expect(requests("workspace_files.resources")).toHaveLength(1);
    expect(screen.queryByRole("button", { name: "Retry" })).not.toBeInTheDocument();
  });
  it("loads only expanded nodes and pages with the returned cursor", async () => {
    setup();
    await screen.findByRole("button", { name: "note.txt" });
    expect(
      requests("workspace_files.list").map((frame) => frame.payload.path),
    ).toEqual(["."]);
    fireEvent.click(screen.getByRole("button", { name: "nested" }));
    await screen.findByRole("button", { name: "child.txt" });
    expect(
      requests("workspace_files.list").map((frame) => frame.payload.path),
    ).toEqual([".", "nested"]);
    fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    await screen.findByRole("button", { name: "second.txt" });
    expect(requests("workspace_files.list").at(-1).payload.cursor).toBe("next");
    fireEvent.click(screen.getByRole("button", { name: "nested" }));
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
  it("binding event invalidates the open file and rejects late read frames", async () => {
    setup();
    await screen.findByRole("button", { name: "note.txt" });
    state.holdRead = true;
    fireEvent.doubleClick(screen.getByRole("button", { name: "note.txt" }));
    await waitFor(() =>
      expect(requests("workspace_files.read")).toHaveLength(1),
    );
    const id = requests("workspace_files.read")[0].payload.client_req_id;
    act(() => emit("project_resource:updated", {}));
    await screen.findByText(/The directory binding changed/);
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
  it("disconnect retires the pending preview response", async () => {
    setup();
    await screen.findByRole("button", { name: "note.txt" });
    state.holdRead = true;
    fireEvent.doubleClick(screen.getByRole("button", { name: "note.txt" }));
    await waitFor(() => expect(requests("workspace_files.read")).toHaveLength(1));
    const id = requests("workspace_files.read")[0].payload.client_req_id;
    act(() => { state.connected = false; state.ready(); });
    await screen.findAllByText("Connecting to the directory channel…");
    act(() => emit("workspace_files.read_chunk", {
      client_req_id: id, resource_id: "resource", seq: 0,
      data: btoa("retired preview content"), eof: true,
    }));
    expect(screen.queryByText("retired preview content")).not.toBeInTheDocument();
    expect(requests("workspace_files.read")).toHaveLength(1);
  });
  it("closing the editor retires an active preview before directory refresh", async () => {
    setup();
    await screen.findByRole("button", { name: "note.txt" });
    state.holdRead = true;
    fireEvent.doubleClick(screen.getByRole("button", { name: "note.txt" }));
    await waitFor(() =>
      expect(requests("workspace_files.read")).toHaveLength(1),
    );
    const id = requests("workspace_files.read")[0].payload.client_req_id;
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    await screen.findByRole("button", { name: "note.txt" });
    expect(
      requests("workspace_files.cancel").some(
        (frame) => frame.payload.client_req_id === id,
      ),
    ).toBe(true);
    expect(requests("workspace_files.read")).toHaveLength(1);
  });
  async function openDraft() {
    setup();
    fireEvent.doubleClick(await screen.findByRole("button", { name: "note.txt" }));
    await screen.findByRole("dialog", { name: "note.txt" });
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    return screen.findByRole("textbox", { name: "File draft" });
  }

  it("retains a conflicted draft until the user reads and accepts the current disk version", async () => {
    const draft = await openDraft();
    fireEvent.change(draft, { target: { value: "my edits" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByText(/The file changed on disk/);
    expect(draft).toHaveValue("my edits");
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    state.editText = "external changes";
    fireEvent.click(screen.getByRole("button", { name: "Read disk version" }));
    await screen.findByText("external changes");
    expect(requests("workspace_files.save")).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "Use this version as the save base" }));
    expect(draft).toHaveValue("my edits");
    expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    await screen.findByRole("button", { name: "Discard draft" });
    fireEvent.click(screen.getByRole("button", { name: "Keep editing" }));
    expect(draft).toHaveValue("my edits");
  });

  it("keeps the draft and ignores a late receipt after disconnection", async () => {
    state.holdSave = true;
    const draft = await openDraft();
    fireEvent.change(draft, { target: { value: "my edits" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    const sent = requests("workspace_files.save")[0].payload;
    act(() => { state.connected = false; state.ready(); });
    await screen.findByText(/Save unconfirmed/);
    act(() => {
      state.connected = true;
      state.ready();
      emit("workspace_files.edit_result", {
        client_req_id: sent.client_req_id, resource_id: "resource", binding_generation: 1,
        version: "B".repeat(43), data: sent.data,
      });
    });
    expect(draft).toHaveValue("my edits");
    expect(screen.getByText(/Save unconfirmed/)).toBeInTheDocument();
    expect(requests("workspace_files.save")).toHaveLength(1);
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("keeps typing done after the submitted draft when a matching save receipt arrives", async () => {
    state.holdSave = true;
    const draft = await openDraft();
    fireEvent.change(draft, { target: { value: "submitted" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    const sent = requests("workspace_files.save")[0].payload;
    fireEvent.change(draft, { target: { value: "more edits" } });
    act(() => emit("workspace_files.edit_result", {
      client_req_id: sent.client_req_id, resource_id: "resource", binding_generation: 1,
      version: "B".repeat(43), data: sent.data,
    }));
    await screen.findByText("Saved.");
    expect(draft).toHaveValue("more edits");
    expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
    expect(requests("workspace_files.save")).toHaveLength(1);
  });
});
