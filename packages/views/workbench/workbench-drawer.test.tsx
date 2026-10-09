import React from "react";
import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { focusManager, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { PluginInstallation } from "@multica/core/types";
import { renderWithI18n } from "../test/i18n";
import { WorkbenchDrawer } from "./workbench-drawer";

const state = vi.hoisted(() => ({
  enabled: true, desktop: false, plugins: [] as PluginInstallation[],
  list: vi.fn(), launch: vi.fn(), close: vi.fn(), connect: vi.fn(), bridgeOptions: vi.fn(),
  send: vi.fn(), subscribe: vi.fn(() => () => {}), onReconnect: vi.fn(() => () => {}),
}));
vi.mock("@multica/core/config", () => ({ useFeatureEnabled: () => state.enabled }));
vi.mock("../platform/local-directory", () => ({ isDesktopShell: () => state.desktop }));
vi.mock("@multica/core/api", () => ({ api: { listPluginInstallations: (...args: unknown[]) => state.list(...args), getPluginSurfaceLaunch: (...args: unknown[]) => state.launch(...args) } }));
vi.mock("@multica/core/realtime", () => ({ useWS: () => ({ send: state.send, subscribe: state.subscribe, onReconnect: state.onReconnect }) }));
vi.mock("../plugins/surface-bridge", () => ({ createSurfaceBridge: (options: unknown) => { state.bridgeOptions(options); return { close: state.close, connect: state.connect }; } }));

function setup(context: React.ComponentProps<typeof WorkbenchDrawer>["context"] = { kind: "issue", issue_id: "issue-1" }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = renderWithI18n(<QueryClientProvider client={client}><WorkbenchDrawer wsId="ws-1" context={context} hasProject={false} /></QueryClientProvider>);
  return { ...view, client, rerenderEntry: (wsId: string, next: React.ComponentProps<typeof WorkbenchDrawer>["context"]) => view.rerender(<QueryClientProvider client={client}><WorkbenchDrawer wsId={wsId} context={next} hasProject={false} /></QueryClientProvider>) };
}
beforeEach(() => {
  vi.clearAllMocks();
  state.enabled = true; state.desktop = false;
  state.plugins = [{ id: "installation-1", plugin_key: "example", name: "Example", version: "1.0.0", enabled: true, package_version_id: "v1", granted_scopes: [], config_schema: [], config: {}, configured_secrets: [], hooks: [], resources: [], created_at: "", updated_at: "", surfaces: [{ key: "s", type: "sidebar_panel", name: "Sidebar", entry: "ui/main.js", platforms: [] }] }];
  state.list.mockImplementation(async () => ({ plugins: state.plugins }));
  state.launch.mockResolvedValue({ url: "https://plugin-content.example.test/plugin-surfaces/fixture", bridge_token: "fixture", version: "1", digest: "digest" });
});

describe("workbench lifecycle", () => {
  for (const desktop of [false, true]) {
    for (const context of [{ kind: "issue", issue_id: "issue-1" }, { kind: "project", project_id: "project-1" }] as const) {
      it(`${desktop ? "desktop" : "web"}/${context.kind} mounts the shared host and passes only real Issue context`, async () => {
        state.desktop = desktop;
        setup(context);
        const user = userEvent.setup();
        await user.click(screen.getByRole("button", { name: "Workbench" }));
        await user.click(await screen.findByRole("tab", { name: "Sidebar" }));
        const frame = await screen.findByTitle("Example — Sidebar");
        expect(frame.getAttribute("srcdoc")).toContain('child.setAttribute("sandbox", "allow-scripts")');
        expect(state.bridgeOptions).toHaveBeenLastCalledWith(expect.objectContaining({ issueId: context.kind === "issue" ? context.issue_id : undefined }));
      });
    }
  }
  it("opens with keyboard and restores focus after Escape", async () => {
    const user = userEvent.setup();
    setup();
    const trigger = screen.getByRole("button", { name: "Workbench" });
    trigger.focus();
    await user.keyboard("{Enter}");
    expect(await screen.findByRole("dialog", { name: "Workbench" })).toBeInTheDocument();
    expect(screen.getByText("This issue has no project.")).toBeInTheDocument();
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(trigger).toHaveFocus();
  });
  it("sidebar disable closes existing bridge and removes iframe", async () => {
    const user = userEvent.setup();
    const { client } = setup();
    await user.click(screen.getByRole("button", { name: "Workbench" }));
    await user.click(await screen.findByRole("tab", { name: "Sidebar" }));
    expect(await screen.findByTitle("Example — Sidebar")).toBeInTheDocument();
    state.close.mockClear();
    state.plugins = [{ ...state.plugins[0]!, enabled: false }];
    await act(async () => { await client.invalidateQueries({ queryKey: ["workspaces", "ws-1", "plugins"] }); });
    await waitFor(() => expect(screen.queryByTitle("Example — Sidebar")).not.toBeInTheDocument());
    expect(state.close).toHaveBeenCalled();
    expect(screen.queryByRole("tab", { name: "Sidebar" })).not.toBeInTheDocument();
  });
  it("observes disable through the five-second foreground refresh", async () => {
    const user = userEvent.setup();
    setup();
    await user.click(screen.getByRole("button", { name: "Workbench" }));
    await user.click(await screen.findByRole("tab", { name: "Sidebar" }));
    await screen.findByTitle("Example — Sidebar");
    state.close.mockClear();
    state.plugins = [{ ...state.plugins[0]!, enabled: false }];
    await waitFor(() => expect(screen.queryByTitle("Example — Sidebar")).not.toBeInTheDocument(), { timeout: 6500 });
    expect(state.close).toHaveBeenCalled();
  }, 10000);
  it("refreshes the installed list when the client regains focus", async () => {
    const user = userEvent.setup();
    setup();
    await user.click(screen.getByRole("button", { name: "Workbench" }));
    await user.click(await screen.findByRole("tab", { name: "Sidebar" }));
    await screen.findByTitle("Example — Sidebar");
    state.close.mockClear();
    state.plugins = [{ ...state.plugins[0]!, enabled: false }];
    try {
      act(() => { focusManager.setFocused(false); focusManager.setFocused(true); });
      await waitFor(() => expect(screen.queryByTitle("Example — Sidebar")).not.toBeInTheDocument());
      expect(state.close).toHaveBeenCalled();
    } finally { focusManager.setFocused(undefined); }
  });
  it("removes a running plugin if list refresh fails", async () => {
    const user = userEvent.setup();
    const { client } = setup();
    await user.click(screen.getByRole("button", { name: "Workbench" }));
    await user.click(await screen.findByRole("tab", { name: "Sidebar" }));
    await screen.findByTitle("Example — Sidebar");
    state.close.mockClear(); state.list.mockRejectedValue(new Error("offline"));
    await act(async () => { await client.invalidateQueries({ queryKey: ["workspaces", "ws-1", "plugins"] }); });
    await waitFor(() => expect(screen.queryByTitle("Example — Sidebar")).not.toBeInTheDocument());
    expect(state.close).toHaveBeenCalled();
  });
  it("cleans up on tab switch, close, entry switch and workspace switch", async () => {
    const user = userEvent.setup();
    const { rerenderEntry } = setup();
    async function mount() {
      await user.click(screen.getByRole("button", { name: "Workbench" }));
      await user.click(await screen.findByRole("tab", { name: "Sidebar" }));
      await screen.findByTitle("Example — Sidebar");
      state.close.mockClear();
    }
    await mount();
    await user.click(screen.getByRole("tab", { name: "Directory status" }));
    expect(state.close).toHaveBeenCalled();
    await user.click(screen.getByRole("tab", { name: "Sidebar" })); await screen.findByTitle("Example — Sidebar");
    state.close.mockClear(); await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(state.close).toHaveBeenCalled();
    await mount(); rerenderEntry("ws-1", { kind: "project", project_id: "project-1" });
    expect(state.close).toHaveBeenCalled(); expect(screen.queryByTitle("Example — Sidebar")).not.toBeInTheDocument();
    await mount(); rerenderEntry("ws-2", { kind: "project", project_id: "project-1" });
    expect(state.close).toHaveBeenCalled(); expect(screen.queryByTitle("Example — Sidebar")).not.toBeInTheDocument();
  });
  it("does not launch plugins when the feature flag is off", async () => {
    state.enabled = false;
    setup(); await userEvent.setup().click(screen.getByRole("button", { name: "Workbench" }));
    expect(screen.queryByRole("tab", { name: "Sidebar" })).not.toBeInTheDocument();
    expect(state.launch).not.toHaveBeenCalled();
  });
});
