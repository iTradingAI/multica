import { DocsHomePage } from "@multica/views/docs";
import { DocRoute, DocHistoryRoute, DocFolderRoute } from "./pages/doc-pages";
import { lazy, useLayoutEffect } from "react";
import { createMemoryRouter, Outlet, useLocation } from "react-router-dom";
import type { RouteObject } from "react-router-dom";

import { WorkspaceRouteLayout } from "./components/workspace-route-layout";
import { DesktopRouteErrorPage } from "./components/route-error-page";
import { loadPage } from "./platform/lazy-page";
import { IssueViewUrlSync } from "./platform/issue-view-tabs";
import { useRenderedRouteStore } from "./stores/rendered-route-store";

const WORKSPACE_VIEW_SCOPE = { scope_type: "workspace" } as const;
const MY_VIEW_SCOPE = { scope_type: "my" } as const;

const IssueDetailPage = lazy(() =>
  loadPage(import("./pages/issue-detail-page").then((m) => ({ default: m.IssueDetailPage }))),
);
const ProjectDetailPage = lazy(() =>
  loadPage(import("./pages/project-detail-page").then((m) => ({ default: m.ProjectDetailPage }))),
);
const AutopilotDetailPage = lazy(() =>
  loadPage(import("./pages/autopilot-detail-page").then((m) => ({ default: m.AutopilotDetailPage }))),
);
const AppsPage = lazy(() =>
  loadPage(import("@multica/views/apps").then((m) => ({ default: m.AppsPage }))),
);
const AppDetailPage = lazy(() =>
  loadPage(import("./pages/app-detail-page").then((m) => ({ default: m.AppDetailPage }))),
);
const SkillDetailPage = lazy(() =>
  loadPage(import("./pages/skill-detail-page").then((m) => ({ default: m.SkillDetailPage }))),
);
const AgentDetailPage = lazy(() =>
  loadPage(import("./pages/agent-detail-page").then((m) => ({ default: m.AgentDetailPage }))),
);
const AiBuilderSessionPage = lazy(() =>
  loadPage(import("./pages/ai-builder-session-page").then((m) => ({ default: m.AiBuilderSessionPage }))),
);
const MemberDetailPage = lazy(() =>
  loadPage(import("./pages/member-detail-page").then((m) => ({ default: m.MemberDetailPage }))),
);
const RuntimeDetailPage = lazy(() =>
  loadPage(import("./pages/runtime-detail-page").then((m) => ({ default: m.RuntimeDetailPage }))),
);
const RuntimeSettingsPage = lazy(() =>
  loadPage(import("./pages/runtime-detail-page").then((m) => ({ default: m.RuntimeSettingsPage }))),
);
const AttachmentPreviewRoute = lazy(() =>
  loadPage(import("./pages/attachment-preview-page").then((m) => ({ default: m.AttachmentPreviewRoute }))),
);
const IssuesPage = lazy(() =>
  loadPage(import("@multica/views/issues/components").then((m) => ({ default: m.IssuesPage }))),
);
const ProjectsPage = lazy(() =>
  loadPage(import("@multica/views/projects/components").then((m) => ({ default: m.ProjectsPage }))),
);
const DashboardPage = lazy(() =>
  loadPage(import("@multica/views/dashboard").then((m) => ({ default: m.DashboardPage }))),
);
const AutopilotsPage = lazy(() =>
  loadPage(import("@multica/views/autopilots/components").then((m) => ({ default: m.AutopilotsPage }))),
);
const MyIssuesPage = lazy(() =>
  loadPage(import("@multica/views/my-issues").then((m) => ({ default: m.MyIssuesPage }))),
);
const SkillsPage = lazy(() =>
  loadPage(import("@multica/views/skills").then((m) => ({ default: m.SkillsPage }))),
);
const DesktopRuntimesPage = lazy(() =>
  loadPage(import("./components/desktop-runtimes-page").then((m) => ({ default: m.DesktopRuntimesPage }))),
);
const DesktopAgentsPage = lazy(() =>
  loadPage(import("./components/desktop-agents-page").then((m) => ({ default: m.DesktopAgentsPage }))),
);
const DesktopChooseRuntimePage = lazy(() =>
  loadPage(import("./components/desktop-choose-runtime-page").then((m) => ({ default: m.DesktopChooseRuntimePage }))),
);
const AgentCreatedPage = lazy(() =>
  loadPage(import("./pages/agent-created-page").then((m) => ({ default: m.AgentCreatedPage }))),
);
const ManualCreateAgentPage = lazy(() =>
  loadPage(import("@multica/views/agents/manual-create-agent-page").then((m) => ({ default: m.ManualCreateAgentPage }))),
);
const SquadsPage = lazy(() =>
  loadPage(import("@multica/views/squads/components").then((m) => ({ default: m.SquadsPage }))),
);
const SquadDetailPageView = lazy(() =>
  loadPage(import("@multica/views/squads/components").then((m) => ({ default: m.SquadDetailPage }))),
);
const AllViewsPage = lazy(() =>
  loadPage(import("@multica/views/issue-views").then((m) => ({ default: m.AllViewsPage }))),
);
const InboxPage = lazy(() =>
  loadPage(import("@multica/views/inbox").then((m) => ({ default: m.InboxPage }))),
);
const ChatPage = lazy(() =>
  loadPage(import("@multica/views/chat").then((m) => ({ default: m.ChatPage }))),
);
const DesktopSettingsRoute = lazy(() =>
  loadPage(import("./pages/settings-page").then((m) => ({ default: m.DesktopSettingsRoute }))),
);

/**
 * Publishes the committed pathname. A layout effect, so the shell's update
 * lands in the same frame as the page it describes.
 */
function RenderedPathnameSync() {
  const { pathname } = useLocation();
  const setPathname = useRenderedRouteStore((s) => s.setPathname);
  useLayoutEffect(() => {
    setPathname(pathname);
  }, [pathname, setPathname]);
  return null;
}

function PageShell() {
  return (
    <>
      <RenderedPathnameSync />
      <Outlet />
    </>
  );
}

/**
 * Route definitions shared by all tabs.
 *
 * Every tab path is workspace-scoped: `/{slug}/{route}/...`. Pre-workspace
 * flows (create workspace, accept invite) are NOT routes — they render as a
 * window-level overlay via `WindowOverlay`, dispatched by the navigation
 * adapter's transition-path interception. The `activeWorkspaceSlug` in the
 * tab store decides which workspace's tabs are visible in the TabBar;
 * workspace-less state (zero-workspace user) shows the overlay instead.
 *
 * The root index route stays as a harmless safety net. With per-workspace
 * tabs, nothing should construct a tab at `/` — but if one ever slips
 * through (malformed persisted state that dodges the migration, direct
 * router.navigate from unforeseen code), the index falls back to null
 * rather than 404; App.tsx's bootstrap repoints activeWorkspaceSlug on the
 * next render pass.
 *
 * Routes carry no titles. Tab and window titles are resolved from the URL by
 * `parseTabSubject` (@multica/core/paths), so a new route segment needs a case
 * there or it renders as "Unknown page" (routes.test.ts enforces this).
 */
export const appRoutes: RouteObject[] = [
  {
    element: <PageShell />,
    errorElement: <DesktopRouteErrorPage />,
    children: [
      { index: true, element: null },
      {
        path: ":workspaceSlug",
        element: <WorkspaceRouteLayout />,
        children: [
          // A bare `/{slug}` URL is normalized to `/{slug}/issues` by
          // sanitizeTabPath before it ever becomes a session, so the index
          // route is unreachable in practice; null keeps it a harmless
          // safety net instead of an in-router <Navigate> (MUL-4741
          // invariant 1: the router never self-navigates).
          { index: true, element: null },
          {
            path: "issues",
            element: (
              <IssueViewUrlSync scope={WORKSPACE_VIEW_SCOPE}>
                <IssuesPage />
              </IssueViewUrlSync>
            ),
          },
          { path: "issues/:id", element: <IssueDetailPage /> },
          {
            // The issue, opened on the newest version of one deliverable.
            path: "issues/:id/deliverables/:name",
            element: <IssueDetailPage />,
          },
          { path: "projects", element: <ProjectsPage /> },
          { path: "projects/:id", element: <ProjectDetailPage /> },
          { path: "autopilots", element: <AutopilotsPage /> },
          { path: "autopilots/:id", element: <AutopilotDetailPage /> },
          {
            path: "my-issues",
            element: (
              <IssueViewUrlSync scope={MY_VIEW_SCOPE}>
                <MyIssuesPage />
              </IssueViewUrlSync>
            ),
          },
          { path: "runtimes", element: <DesktopRuntimesPage /> },
          { path: "runtimes/:id", element: <RuntimeDetailPage /> },
          {
            path: "runtimes/:id/runtime/:runtimeId",
            element: <RuntimeSettingsPage />,
          },
          { path: "docs", element: <DocsHomePage /> },
          { path: "docs/folders/:id", element: <DocFolderRoute /> },
          { path: "docs/:id", element: <DocRoute /> },
          { path: "docs/:id/history", element: <DocHistoryRoute /> },
          { path: "skills", element: <SkillsPage /> },
          { path: "skills/:id", element: <SkillDetailPage /> },
          { path: "apps", element: <AppsPage /> },
          { path: "apps/:slug", element: <AppDetailPage /> },
          { path: "agents", element: <DesktopAgentsPage /> },
          { path: "agents/new", element: <DesktopChooseRuntimePage /> },
          { path: "agents/new/done/:agentId", element: <AgentCreatedPage /> },
          { path: "agents/new/manual", element: <ManualCreateAgentPage /> },
          { path: "agents/new/ai/:sessionId", element: <AiBuilderSessionPage /> },
          { path: "agents/:id", element: <AgentDetailPage /> },
          { path: "members/:id", element: <MemberDetailPage /> },
          { path: "squads", element: <SquadsPage /> },
          { path: "views", element: <AllViewsPage /> },
          { path: "squads/:id", element: <SquadDetailPageView /> },
          { path: "inbox", element: <InboxPage /> },
          { path: "chat", element: <ChatPage /> },
          { path: "attachments/:id/preview", element: <AttachmentPreviewRoute /> },
          { path: "usage", element: <DashboardPage /> },
          { path: "settings", element: <DesktopSettingsRoute /> },
        ].map((route) => ({
          ...route,
          // Keep failed leaves inside the workspace owner. Bubbling to the
          // root unmounts it and lets the shell's workspace gate hide recovery.
          errorElement: <DesktopRouteErrorPage />,
        })),
      },
    ],
  },
];

/**
 * Create THE app router (MUL-4741 single-router session architecture).
 * There is exactly one instance, owned by the tab Coordinator; it projects
 * the active tab session's URL and is never navigated by anything else.
 */
export function createAppRouter() {
  return createMemoryRouter(appRoutes, {
    initialEntries: ["/"],
  });
}
