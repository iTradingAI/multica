"use client";

import type { PluginInstallation, PluginSurface } from "@multica/core/types";
import type { WorkbenchContext } from "@multica/core/workspace-files";
import { PluginSurfaceFrame } from "../plugins/plugin-surface-frame";

export interface SidebarPanel { id: string; label: string; installation: PluginInstallation; surface: PluginSurface }
export function sidebarPanels(installations: PluginInstallation[], platform: "web" | "desktop"): SidebarPanel[] {
  return installations.filter((installation) => installation.enabled === true).flatMap((installation) =>
    installation.surfaces.filter((surface) => surface.type === "sidebar_panel" &&
      ((surface.platforms ?? []).length === 0 || surface.platforms?.includes(platform)))
      .map((surface) => ({ id: `${installation.id}:${surface.key}`, label: surface.name, installation, surface })),
  );
}

export function SidebarPanel({ wsId, context, panel }: { wsId: string; context: WorkbenchContext; panel: SidebarPanel }) {
  // Project surfaces receive no synthetic Issue or additional Action authority.
  const key = JSON.stringify([wsId, context.kind, context.kind === "issue" ? context.issue_id : context.project_id, panel.installation.id, panel.installation.package_version_id, panel.surface.key]);
  return <PluginSurfaceFrame key={key} wsId={wsId} installation={panel.installation} surface={panel.surface} issueId={context.kind === "issue" ? context.issue_id : undefined} />;
}
