import { lazy, useEffect } from "react";
import { createMemoryRouter, Outlet, useMatches } from "react-router-dom";
import type { RouteObject } from "react-router-dom";
import { WorkspaceRouteLayout } from "./components/workspace-route-layout";
import { DesktopRouteErrorPage } from "./components/route-error-page";
import { loadPage } from "./platform/lazy-page";

const IssueDetailPage = lazy(() =>
  loadPage(import("./pages/issue-detail-page").then((m) => ({ default: m.IssueDetailPage }))),
);
const ProjectDetailPage = lazy(() =>
  loadPage(import("./pages/project-detail-page").then((m) => ({ default: m.ProjectDetailPage }))),
);
const AutopilotDetailPage = lazy(() =>
  loadPage(import("./pages/autopilot-detail-page").then((m) => ({ default: m.AutopilotDetailPage }))),
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
const AiCreateAgentPage = lazy(() =>
  loadPage(import("@multica/views/agents/ai-create-agent-page").then((m) => ({ default: m.AiCreateAgentPage }))),
);
const SquadsPage = lazy(() =>
  loadPage(import("@multica/views/squads/components").then((m) => ({ default: m.SquadsPage }))),
);
const SquadDetailPageView = lazy(() =>
  loadPage(import("@multica/views/squads/components").then((m) => ({ default: m.SquadDetailPage }))),
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
 * Sets document.title from the deepest matched route's handle.title.
 * The tab system observes document.title via MutationObserver.
 * Pages with dynamic titles (e.g. issue detail) override by setting
 * document.title directly via useDocumentTitle().
 */
function TitleSync() {
  const matches = useMatches();
  const title = [...matches]
    .reverse()
    .find((m) => (m.handle as { title?: string })?.title)
    ?.handle as { title?: string } | undefined;

  useEffect(() => {
    if (title?.title) document.title = title.title;
  }, [title?.title]);

  return null;
}

/** Wrapper that renders route children + TitleSync */
function PageShell() {
  return (
    <>
      <TitleSync />
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
            element: <IssuesPage />,
            handle: { title: "Issues" },
          },
          {
            path: "issues/:id",
            element: <IssueDetailPage />,
            handle: { title: "Issue" },
          },
          {
            path: "projects",
            element: <ProjectsPage />,
            handle: { title: "Projects" },
          },
          {
            path: "projects/:id",
            element: <ProjectDetailPage />,
            handle: { title: "Project" },
          },
          {
            path: "autopilots",
            element: <AutopilotsPage />,
            handle: { title: "Autopilot" },
          },
          {
            path: "autopilots/:id",
            element: <AutopilotDetailPage />,
            handle: { title: "Autopilot" },
          },
          {
            path: "my-issues",
            element: <MyIssuesPage />,
            handle: { title: "My Issues" },
          },
          {
            path: "runtimes",
            element: <DesktopRuntimesPage />,
            handle: { title: "Runtimes" },
          },
          {
            path: "runtimes/:id",
            element: <RuntimeDetailPage />,
            handle: { title: "Machine" },
          },
          {
            path: "runtimes/:id/runtime/:runtimeId",
            element: <RuntimeSettingsPage />,
            handle: { title: "Runtime" },
          },
          { path: "skills", element: <SkillsPage />, handle: { title: "Skills" } },
          {
            path: "skills/:id",
            element: <SkillDetailPage />,
            handle: { title: "Skill" },
          },
          { path: "agents", element: <DesktopAgentsPage />, handle: { title: "Agents" } },
          {
            path: "agents/new",
            element: <DesktopChooseRuntimePage />,
            handle: { title: "Create Agent" },
          },
          {
            path: "agents/new/done/:agentId",
            element: <AgentCreatedPage />,
            handle: { title: "Create Agent" },
          },
          {
            path: "agents/new/manual",
            element: <ManualCreateAgentPage />,
            handle: { title: "Create Agent" },
          },
          {
            path: "agents/new/ai",
            element: <AiCreateAgentPage />,
            handle: { title: "Create Agent" },
          },
          {
            path: "agents/new/ai/:sessionId",
            element: <AiBuilderSessionPage />,
            handle: { title: "Create Agent" },
          },
          {
            path: "agents/:id",
            element: <AgentDetailPage />,
            handle: { title: "Agent" },
          },
          {
            path: "members/:id",
            element: <MemberDetailPage />,
            handle: { title: "Member" },
          },
          { path: "squads", element: <SquadsPage />, handle: { title: "Squads" } },
          {
            path: "squads/:id",
            element: <SquadDetailPageView />,
            handle: { title: "Squad" },
          },
          { path: "inbox", element: <InboxPage />, handle: { title: "Inbox" } },
          { path: "chat", element: <ChatPage />, handle: { title: "Chat" } },
          {
            path: "attachments/:id/preview",
            element: <AttachmentPreviewRoute />,
            handle: { title: "Attachment" },
          },
          {
            path: "usage",
            element: <DashboardPage />,
            handle: { title: "Usage" },
          },
          {
            path: "settings",
            element: <DesktopSettingsRoute />,
            handle: { title: "Settings" },
          },
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
