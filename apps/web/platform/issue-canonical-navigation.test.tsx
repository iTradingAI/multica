import { useState } from "react";
import { act, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../../packages/views/test/i18n";
import { WorkbenchDrawer } from "../../../packages/views/workbench/workbench-drawer";
import { useCanonicalIssueUrl } from "../../../packages/views/issues/components/issue-detail-route";
import { WebNavigationProvider } from "./navigation";

const router = vi.hoisted(() => ({ push: vi.fn(), replace: vi.fn(), back: vi.fn(), prefetch: vi.fn() }));
const ws = vi.hoisted(() => ({ send: vi.fn(), subscribe: () => () => {}, onReady: () => () => {} }));
vi.mock("next/navigation", () => ({
  useRouter: () => router,
  usePathname: () => window.location.pathname,
  useSearchParams: () => new URLSearchParams(),
}));
vi.mock("@multica/core/config", () => ({ useFeatureEnabled: () => false }));
vi.mock("@multica/core/paths", async () => {
  const actual = await vi.importActual<typeof import("@multica/core/paths")>("@multica/core/paths");
  return { ...actual, useWorkspacePaths: () => actual.paths.workspace("acme") };
});
vi.mock("@multica/core/realtime", () => ({
  useWS: () => ws,
}));

const uuid = "cb240efb-154c-42a8-ae92-42b02676feca";

describe("issue URL canonicalization preserves the web page session", () => {
  it("keeps the open drawer and selected sub-issues tab during UUID → identifier replace", async () => {
    window.history.replaceState(null, "", `/acme/issues/${uuid}#comment-c1`);
    const historyLength = window.history.length;
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    let resolveIdentifier: () => void = () => {};
    function Page() {
      const [identifier, setIdentifier] = useState<string>();
      resolveIdentifier = () => setIdentifier("TES-1");
      useCanonicalIssueUrl(uuid, identifier, "#comment-c1");
      return <WorkbenchDrawer wsId="ws-1" context={{ kind: "issue", issue_id: uuid }} hasProject={false} issueSubIssues={<div>TES-2 Child</div>} />;
    }
    function Route() {
      const [segment, setSegment] = useState(uuid);
      // Next navigation to another dynamic segment replaces the page subtree.
      // Same-page history replacement must never take this navigation path.
      router.replace.mockImplementation((path: string) => setSegment(path));
      return <Page key={segment} />;
    }
    renderWithI18n(<QueryClientProvider client={client}><WebNavigationProvider><Route /></WebNavigationProvider></QueryClientProvider>);
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Workbench" }));
    await user.click(screen.getByRole("tab", { name: "Sub-issues" }));
    act(() => resolveIdentifier());

    expect(screen.getByRole("dialog", { name: "Workbench" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Sub-issues" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText("TES-2 Child")).toBeInTheDocument();
    expect(window.location.pathname + window.location.hash).toBe("/acme/issues/TES-1#comment-c1");
    expect(window.history.length).toBe(historyLength);
    expect(router.replace).not.toHaveBeenCalled();
    client.clear();
  });
});
