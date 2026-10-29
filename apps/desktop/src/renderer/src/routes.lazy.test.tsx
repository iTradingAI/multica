import { useEffect } from "react";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "react-router-dom";
import { useWorkspaceSlug } from "@multica/core/paths";
import { getCurrentSlug } from "@multica/core/platform";

const state = vi.hoisted(() => ({
  inboxImports: 0,
  inboxMounts: 0,
  lateProjectMounts: 0,
  pageSlugs: [] as string[],
  release: () => {},
  releaseProject: () => {},
  gate: null as Promise<void> | null,
  projectGate: null as Promise<void> | null,
}));
state.gate = new Promise<void>((resolve) => { state.release = resolve; });
state.projectGate = new Promise<void>((resolve) => { state.releaseProject = resolve; });

vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (s: { user: { id: string }; isLoading: boolean }) => unknown) =>
    selector({ user: { id: "test-user" }, isLoading: false }),
}));
vi.mock("@multica/core/workspace", () => ({
  workspaceBySlugOptions: (slug: string) => ({
    queryKey: ["test-workspace", slug],
    queryFn: async () => ({ id: slug, slug }),
    initialData: { id: slug, slug },
  }),
  workspaceListOptions: () => ({
    queryKey: ["test-workspaces"],
    queryFn: async () => [{ id: "acme", slug: "acme" }, { id: "other", slug: "other" }],
    initialData: [{ id: "acme", slug: "acme" }, { id: "other", slug: "other" }],
  }),
}));
vi.mock("@multica/views/workspace/use-workspace-seen", () => ({ useWorkspaceSeen: () => true }));
vi.mock("@multica/views/layout", () => ({ WorkspacePresencePrefetch: () => null }));
vi.mock("@multica/views/onboarding", () => ({ SourceBackfillModal: () => null }));
vi.mock("@multica/views/issues/components", () => ({ IssuesPage: () => <div>Issues fixture</div> }));
vi.mock("@multica/views/inbox", async () => {
  state.inboxImports++;
  await state.gate;
  return {
    InboxPage: function InboxFixture() {
      const slug = useWorkspaceSlug();
      useEffect(() => { state.inboxMounts++; state.pageSlugs.push(slug!); }, [slug]);
      return <div>Inbox fixture {slug}<input aria-label="Inbox draft" /></div>;
    },
  };
});
vi.mock("@multica/views/skills", () => { throw new Error("test chunk unavailable"); });
vi.mock("@multica/views/projects/components", async () => {
  await state.projectGate;
  return { ProjectsPage: function ProjectsFixture() {
    useEffect(() => { state.lateProjectMounts++; }, []);
    return <div>Projects fixture</div>;
  } };
});

import { TabContent } from "./components/tab-content";
import { getActiveTab, useTabStore } from "./stores/tab-store";
import { getAppRouter } from "./platform/tab-coordinator";
import { createAppRouter } from "./routes";

afterEach(() => { vi.restoreAllMocks(); });

describe("desktop lazy routes", () => {
  it("defers inbox, preserves the shell and memento through a cold workspace switch, and caches revisits", async () => {
    useTabStore.getState().reset();
    useTabStore.getState().switchWorkspace("acme");
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
    const view = render(
      <QueryClientProvider client={qc}>
        <button type="button" onClick={() => useTabStore.getState().switchWorkspace("other")}>Other workspace</button>
        <TabContent />
      </QueryClientProvider>,
    );
    await screen.findByText("Issues fixture");
    expect(state.inboxImports).toBe(0);
    let inboxId = "";
    act(() => {
      inboxId = useTabStore.getState().openTab("/acme/inbox?issue=one#comment-one", "Inbox", { activate: true });
      useTabStore.getState().commitScrollMemento(inboxId, "/acme/inbox", { main: { top: 420, height: 2000 } });
    });
    await screen.findByRole("status", { name: "Loading page" });
    expect(state.inboxImports).toBe(1);
    expect(document.title).toBe("Inbox");
    act(() => { useTabStore.getState().reloadActiveTab(); });
    await screen.findByRole("status", { name: "Loading page" });
    expect(useTabStore.getState().byWorkspace.acme.tabs.find((tab) => tab.id === inboxId)!
      .memento.scroll["/acme/inbox::main"]?.top).toBe(420);
    act(() => { useTabStore.getState().closeTab(inboxId); });
    await screen.findByText("Issues fixture");
    act(() => {
      inboxId = useTabStore.getState().openTab("/acme/inbox?issue=one#comment-one", "Inbox", { activate: true });
      useTabStore.getState().commitScrollMemento(inboxId, "/acme/inbox", { main: { top: 420, height: 2000 } });
    });
    await screen.findByRole("status", { name: "Loading page" });
    fireEvent.click(screen.getByRole("button", { name: "Other workspace" }));
    await screen.findByText("Issues fixture");
    const saved = useTabStore.getState().byWorkspace.acme.tabs.find((tab) => tab.id === inboxId)!;
    expect(saved.memento.scroll["/acme/inbox::main"]).toEqual({ top: 420, height: 2000 });
    await act(async () => { state.release(); await state.gate; });
    expect(screen.queryByText(/Inbox fixture/)).not.toBeInTheDocument();
    expect(state.inboxMounts).toBe(0);
    act(() => { useTabStore.getState().switchWorkspace("acme"); });
    await screen.findByText("Inbox fixture acme");
    expect(state.pageSlugs).toEqual(["acme"]);
    const mounts = state.inboxMounts;
    fireEvent.change(screen.getByRole("textbox", { name: "Inbox draft" }), { target: { value: "unsaved" } });
    act(() => { useTabStore.getState().navigateActiveSession("/acme/inbox?issue=two#comment-two"); });
    expect(screen.getByRole("textbox", { name: "Inbox draft" })).toHaveValue("unsaved");
    expect(state.inboxMounts).toBe(mounts);
    act(() => { useTabStore.getState().openTab("/acme/issues", "Issues", { activate: true }); });
    await screen.findByText("Issues fixture");
    act(() => { useTabStore.getState().setActiveTab(inboxId); });
    await screen.findByText("Inbox fixture acme");
    expect(state.inboxImports).toBe(1);
    expect(getActiveTab(useTabStore.getState())?.url).toBe("/acme/inbox?issue=two#comment-two");
    expect(getAppRouter().state.location.pathname).toBe("/acme/inbox");
    view.unmount();
    qc.clear();
  });

  it("routes a rejected import to the existing recovery page without an automatic retry", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    const router = createAppRouter();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
    const view = render(<QueryClientProvider client={qc}><RouterProvider router={router} /></QueryClientProvider>);
    await act(async () => { await router.navigate("/acme/skills"); });
    await screen.findByRole("button", { name: "Reload window" });
    await act(async () => {});
    expect(getCurrentSlug()).toBe("acme");
    expect(screen.getByRole("alert")).toHaveTextContent("entire window");
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    view.unmount();
    router.dispose();
    qc.clear();
  });

  it("does not mount a late module after session teardown resets the tabs", async () => {
    useTabStore.getState().reset();
    useTabStore.getState().switchWorkspace("acme");
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
    const view = render(<QueryClientProvider client={qc}><TabContent /></QueryClientProvider>);
    await screen.findByText("Issues fixture");
    act(() => { useTabStore.getState().navigateActiveSession("/acme/projects"); });
    await screen.findByRole("status", { name: "Loading page" });
    act(() => { useTabStore.getState().reset(); });
    await act(async () => { state.releaseProject(); await state.projectGate; });
    expect(state.lateProjectMounts).toBe(0);
    expect(screen.queryByText("Projects fixture")).not.toBeInTheDocument();
    expect(getAppRouter().state.location.pathname).toBe("/");
    expect(getActiveTab(useTabStore.getState())).toBeNull();
    view.unmount();
    qc.clear();
  });
});
