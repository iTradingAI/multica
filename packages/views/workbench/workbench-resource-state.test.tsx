import { screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { WorkspaceFilesError } from "@multica/core/workspace-files";
import { renderWithI18n } from "../test/i18n";
import { WorkbenchResourceState } from "./workbench-resource-state";

const state = vi.hoisted(() => ({ request: vi.fn(), ws: { subscribe: vi.fn(), send: vi.fn() } }));
vi.mock("@multica/core/realtime", () => ({ useWS: () => state.ws }));
vi.mock("@multica/core/workspace-files", async (original) => ({ ...await original<typeof import("@multica/core/workspace-files")>(), requestWorkspaceFiles: (...args: unknown[]) => state.request(...args) }));
function setup(hasProject = true) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(<QueryClientProvider client={client}><WorkbenchResourceState wsId="ws-1" context={{ kind: "issue", issue_id: "issue-1" }} hasProject={hasProject} /></QueryClientProvider>);
}
beforeEach(() => { state.request.mockReset(); });
describe("controlled directory states (no daemon or SQL claims)", () => {
  it("does not send a request for an Issue without a project", () => {
    setup(false);
    expect(screen.getByText("This issue has no project.")).toBeInTheDocument();
    expect(state.request).not.toHaveBeenCalled();
  });
  it("keeps loading separate from an empty resource list", async () => {
    state.request.mockResolvedValue({ resources: [] });
    setup();
    expect(screen.getByText("Checking directory access…")).toBeInTheDocument();
    expect(await screen.findByText("No local directories are attached to this project.")).toBeInTheDocument();
  });
  for (const [code, message] of Object.entries({ forbidden: "You do not have access to this directory.", unavailable: "The directory channel is unavailable.", daemon_offline: "The directory daemon is offline.", daemon_upgrade_required: "Update the directory daemon to continue.", timeout: "The directory check timed out.", unknown_code: "The directory channel is unavailable." })) {
    it(`maps ${code} without leaking server paths`, async () => {
      state.request.mockRejectedValue(new WorkspaceFilesError(code));
      const { container } = setup();
      expect(await screen.findByText(message)).toBeInTheDocument();
      expect(container.textContent).not.toContain("root_path");
    });
  }
  it("probes the authorized root after resources and never renders server names", async () => {
    state.request.mockResolvedValueOnce({ resources: [{ resource_id: "resource-1", display_name: "private absolute path", access: "read_only" }] }).mockResolvedValueOnce({ entries: [{ name: "private.txt", type: "regular" }] });
    const { container } = setup();
    expect(await screen.findByText("The directory is available.")).toBeInTheDocument();
    expect(state.request.mock.calls[1]?.[1]).toEqual({ operation: "list", context: { kind: "issue", issue_id: "issue-1" }, resourceId: "resource-1", path: "." });
    expect(container.textContent).not.toContain("private");
  });
  it("aborts a pending diagnostic on unmount", async () => {
    let signal: AbortSignal | undefined;
    state.request.mockImplementation((_ws, _request, nextSignal) => { signal = nextSignal; return new Promise(() => {}); });
    const view = setup();
    await waitFor(() => expect(signal).toBeDefined());
    view.unmount();
    expect(signal?.aborted).toBe(true);
  });
});
