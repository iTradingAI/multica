"use client";

import { useQuery } from "@tanstack/react-query";
import { useId } from "react";
import { useWS } from "@multica/core/realtime";
import { requestWorkspaceFiles, WorkspaceFilesError, type WorkbenchContext } from "@multica/core/workspace-files";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../i18n";

export function WorkbenchResourceState({ wsId, scope: hostScope, context, hasProject }: { wsId: string; scope?: string; context: WorkbenchContext; hasProject: boolean }) {
  const instance = useId();
  const scope = hostScope ?? instance;
  const { t } = useT("common");
  const ws = useWS();
  const resources = useQuery({
    queryKey: ["workspaces", wsId, "workbench", scope, "resources", context],
    queryFn: ({ signal }) => requestWorkspaceFiles(ws, { operation: "resources", context }, signal),
    enabled: hasProject && Boolean(wsId), retry: false, gcTime: 0, staleTime: 0, refetchOnMount: "always",
  });
  if (!hasProject) return <p role="status">{t(($) => $.workbench.no_project)}</p>;
  if (resources.isPending) return <p role="status">{t(($) => $.workbench.loading)}</p>;
  if (resources.isError) return <ResourceError error={resources.error} retry={() => { void resources.refetch(); }} />;
  const directories = resources.data.resources ?? [];
  if (directories.length === 0) return <p role="status">{t(($) => $.workbench.no_resources)}</p>;
  return <div className="space-y-4">{directories.map((resource, index) => <div key={resource.resource_id}><p className="mb-2 font-medium">{t(($) => $.workbench.directory, { number: index + 1 })}</p><DirectoryProbe wsId={wsId} scope={scope} context={context} resourceId={resource.resource_id} /></div>)}</div>;
}

function DirectoryProbe({ wsId, scope, context, resourceId }: { wsId: string; scope: string; context: WorkbenchContext; resourceId: string }) {
  const { t } = useT("common");
  const ws = useWS();
  const probe = useQuery({
    queryKey: ["workspaces", wsId, "workbench", scope, "probe", context, resourceId],
    queryFn: ({ signal }) => requestWorkspaceFiles(ws, { operation: "list", context, resourceId, path: "." }, signal),
    retry: false, gcTime: 0, staleTime: 0, refetchOnMount: "always",
  });
  // Resource enumeration is not a daemon readiness check. Probe only the
  // authorized root; names and filesystem paths are never rendered here.
  if (probe.isPending) return <p role="status">{t(($) => $.workbench.loading)}</p>;
  if (probe.isError) return <ResourceError error={probe.error} retry={() => { void probe.refetch(); }} />;
  return <p role="status">{t(($) => $.workbench.ready)}</p>;
}
function ResourceError({ error, retry }: { error: Error; retry: () => void }) {
  const { t } = useT("common");
  const code = error instanceof WorkspaceFilesError ? error.code : "unavailable";
  const message = code === "forbidden" ? t(($) => $.workbench.forbidden)
    : code === "daemon_offline" ? t(($) => $.workbench.daemon_offline)
    : code === "daemon_upgrade_required" ? t(($) => $.workbench.daemon_upgrade_required)
    : code === "timeout" ? t(($) => $.workbench.timeout) : t(($) => $.workbench.unavailable);
  return <div className="space-y-3"><p role="status">{message}</p><Button variant="outline" size="sm" onClick={retry}>{t(($) => $.workbench.retry)}</Button></div>;
}
