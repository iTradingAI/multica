import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { undo } from "@codemirror/commands";
import { EditorView } from "@codemirror/view";
import { renderWithI18n } from "../test/i18n";
import { FilesTab } from "./files-tab";
import { WorkspaceFilesSection } from "./workbench-drawer";

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
  fileName: "note.txt",
  previewText: "selected evidence",
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
const onReady = () => () => {};
vi.mock("@multica/core/realtime", () => ({
  useWS: () => ({ send: state.send, subscribe, subscribeStatus, onReady, getStatus: () => state.connected ? "connected" : "reconnecting" }),
}));
vi.mock("@multica/core/projects", () => ({ projectResourcesOptions: () => ({ queryKey: ["labels"], queryFn: async () => [] }) }));
vi.mock("@multica/core/config", () => ({ useFeatureEnabled: () => false }));
vi.mock("../platform/local-directory", () => ({ isDesktopShell: () => false }));
vi.mock("@multica/core/api", () => ({ api: {
  listIssueFileTouches: async () => ({ files: [] }),
} }));

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
  state.fileName = "note.txt";
  state.previewText = "selected evidence";
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
                    { name: state.fileName, type: "regular" },
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
            data: btoa(state.previewText),
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

const editorView = (element: HTMLElement) => EditorView.findFromDOM(element)!;
const editorText = (element: HTMLElement) => editorView(element).state.sliceDoc();
function replaceDraft(element: HTMLElement, text: string) {
  const view = editorView(element);
  act(() => view.dispatch({
    changes: { from: 0, to: view.state.doc.length, insert: text },
    userEvent: "input",
  }));
}

describe("File drafts on an issue that changes project", () => {
  it.each([false, true])("keeps a copyable draft and retires the old binding (save pending: %s)", async (savePending) => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const renderEntry = (projectId: string | undefined) => (
      <QueryClientProvider client={client}>
        <WorkspaceFilesSection wsId="workspace" context={{ kind: "issue", issue_id: "issue-1" }}
          hasProject={Boolean(projectId)} projectId={projectId} />
      </QueryClientProvider>
    );
    const { rerender } = renderWithI18n(renderEntry("project-1"));
    const copy = vi.fn().mockResolvedValue(undefined);
    const clipboard = Object.getOwnPropertyDescriptor(navigator, "clipboard");
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: copy } });
    try {
      fireEvent.click(screen.getByRole("button", { name: "File management" }));
      const openEditor = async () => {
        fireEvent.doubleClick(await screen.findByRole("button", { name: "note.txt" }));
        fireEvent.click(await screen.findByRole("button", { name: "Edit" }));
        return screen.findByRole("textbox", { name: "File draft" });
      };
      const draft = await openEditor();
      replaceDraft(draft, "old project draft");
      state.holdSave = true;
      if (savePending) fireEvent.click(screen.getByRole("button", { name: "Save" }));
      const sent = requests("workspace_files.save")[0]?.payload;

      rerender(renderEntry("project-2"));
      expect(screen.getByRole("textbox", { name: "File draft" })).toBe(draft);
      expect(draft).toHaveAttribute("contenteditable", "false");
      expect(editorView(draft).state.readOnly).toBe(true);
      expect(screen.getByText(/The directory binding changed/)).toBeInTheDocument();
      if (sent) {
        expect(screen.getByText(/Save unconfirmed/)).toBeInTheDocument();
        expect(requests("workspace_files.cancel").map((frame) => frame.payload.client_req_id))
          .toContain(sent.client_req_id);
        act(() => emit("workspace_files.edit_result", {
          client_req_id: sent.client_req_id, resource_id: "resource", binding_generation: 1,
          version: "B".repeat(43), data: sent.data,
        }));
        expect(screen.getByText(/Save unconfirmed/)).toBeInTheDocument();
        expect(screen.queryByText("Saved.")).not.toBeInTheDocument();
      }
      expect(editorText(draft)).toBe("old project draft");
      fireEvent.click(screen.getByRole("button", { name: "Copy draft" }));
      expect(copy).toHaveBeenCalledWith("old project draft");
      expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
      rerender(renderEntry(undefined));
      expect(editorText(draft)).toBe("old project draft");
      rerender(renderEntry("project-1"));
      expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
      fireEvent.click(screen.getByRole("button", { name: "Save" }));
      expect(requests("workspace_files.save")).toHaveLength(Number(savePending));

      fireEvent.click(screen.getByRole("button", { name: "Close" }));
      fireEvent.click(await screen.findByRole("button", { name: "Discard draft" }));
      rerender(renderEntry("project-2"));
      state.editText = "fresh project text";
      const fresh = await openEditor();
      expect(editorText(fresh)).toBe("fresh project text");
      if (sent) act(() => emit("workspace_files.edit_result", {
        client_req_id: sent.client_req_id, resource_id: "resource", binding_generation: 1,
        version: "B".repeat(43), data: sent.data,
      }));
      expect(editorText(fresh)).toBe("fresh project text");
      replaceDraft(fresh, "new project draft");
      fireEvent.click(screen.getByRole("button", { name: "Save" }));
      expect(requests("workspace_files.save").at(-1).payload.version).toBe("A".repeat(43));
      expect(requests("workspace_files.save")).toHaveLength(Number(savePending) + 1);
    } finally {
      if (clipboard) Object.defineProperty(navigator, "clipboard", clipboard);
      else Reflect.deleteProperty(navigator, "clipboard");
      client.clear();
    }
  });
});

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
    await screen.findAllByText("Connecting to the directory channel...");
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

  it("highlights read-only source and opens the editable draft in the same dialog", async () => {
    state.fileName = "example.TSX";
    state.previewText = "export const element = <span />;";
    state.editText = state.previewText;
    setup();
    fireEvent.doubleClick(await screen.findByRole("button", { name: state.fileName }));
    const dialog = await screen.findByRole("dialog", { name: state.fileName });
    const preview = await screen.findByRole("textbox", { name: state.fileName });
    expect(preview).toHaveAttribute("contenteditable", "false");
    expect(preview).toHaveAttribute("aria-readonly", "true");
    expect(editorView(preview).state.readOnly).toBe(true);
    expect(preview).toHaveTextContent(state.previewText);
    await waitFor(() => expect(screen.getByText("export").tagName).toBe("SPAN"));
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    const draft = await screen.findByRole("textbox", { name: "File draft" });
    expect(dialog).toContainElement(draft);
    expect(screen.getAllByRole("dialog")).toHaveLength(1);
    expect(draft).toHaveAttribute("contenteditable", "true");
    expect(draft).toHaveAttribute("aria-readonly", "false");
    await waitFor(() => expect(screen.getByText("export").tagName).toBe("SPAN"));
  });

  it("tracks dirty state across undo and applies the 16 KiB UTF-8 draft limit", async () => {
    const draft = await openDraft();
    const save = screen.getByRole("button", { name: "Save" });
    expect(save).toBeDisabled();
    replaceDraft(draft, "changed");
    expect(save).toBeEnabled();
    act(() => { undo(editorView(draft)); });
    expect(editorText(draft)).toBe(state.editText);
    expect(save).toBeDisabled();
    // 4-byte Unicode code points reach the byte limit before the character limit.
    const atLimit = "🧪".repeat(4096);
    replaceDraft(draft, atLimit);
    expect(save).toBeEnabled();
    replaceDraft(draft, atLimit + "!");
    expect(save).toBeDisabled();
    expect(requests("workspace_files.save")).toHaveLength(0);
    replaceDraft(draft, state.editText);
    expect(save).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Discard draft" })).not.toBeInTheDocument();
  });

  it("retains a conflicted draft until the user reads and accepts the current disk version", async () => {
    const draft = await openDraft();
    replaceDraft(draft, "my edits");
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByText(/The file changed on disk/);
    expect(editorText(draft)).toBe("my edits");
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    state.editText = "external changes";
    fireEvent.click(screen.getByRole("button", { name: "Read disk version" }));
    await screen.findByText("external changes");
    expect(requests("workspace_files.save")).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "Use this version as the save base" }));
    expect(editorText(draft)).toBe("my edits");
    expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    await screen.findByRole("button", { name: "Discard draft" });
    fireEvent.click(screen.getByRole("button", { name: "Keep editing" }));
    expect(editorText(draft)).toBe("my edits");
  });

  it("keeps the draft and ignores a late receipt after disconnection", async () => {
    state.holdSave = true;
    const draft = await openDraft();
    replaceDraft(draft, "my edits");
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
    expect(editorText(draft)).toBe("my edits");
    expect(screen.getByText(/Save unconfirmed/)).toBeInTheDocument();
    expect(requests("workspace_files.save")).toHaveLength(1);
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("keeps typing done after the submitted draft when a matching save receipt arrives", async () => {
    state.holdSave = true;
    const draft = await openDraft();
    replaceDraft(draft, "submitted");
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    const sent = requests("workspace_files.save")[0].payload;
    replaceDraft(draft, "more edits");
    act(() => emit("workspace_files.edit_result", {
      client_req_id: sent.client_req_id, resource_id: "resource", binding_generation: 1,
      version: "B".repeat(43), data: sent.data,
    }));
    await screen.findByText("Saved.");
    expect(editorText(draft)).toBe("more edits");
    expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
    expect(requests("workspace_files.save")).toHaveLength(1);
  });
});
