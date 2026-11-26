"use client";

import { useEffect, useId, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useFeatureEnabled } from "@multica/core/config";
import { PLUGINS_V1_FLAG } from "@multica/core/feature-flags";
import { pluginInstallationsOptions } from "@multica/core/plugins";
import { useWS } from "@multica/core/realtime";
import type { WorkbenchContext } from "@multica/core/workspace-files";
import { Button } from "@multica/ui/components/ui/button";
import { Tabs, TabsList, TabsTrigger, TabsContent,
} from "@multica/ui/components/ui/tabs";
import { ChevronRight, Folder } from "@multica/ui/icons";
import { useT } from "../i18n";
import { isDesktopShell } from "../platform/local-directory";
import { SidebarPanel, sidebarPanels } from "./sidebar-panel";
import { WorkbenchResourceState } from "./workbench-resource-state";
import { FilesTab } from "./files-tab";

interface Props { wsId: string; context: WorkbenchContext; hasProject?: boolean; projectId?: string;
}
interface WorkbenchTab { id: string; label: string;
}

export function WorkspaceFilesSection(props: Props) {
  // Changing workspace or entry destroys the entire session, including the
  // open state, selected tab, in-flight queries, iframe and MessagePort.
  return (
    <WorkbenchSession key={JSON.stringify([props.wsId, props.context, props.projectId])} {...props} />
  );
}
function WorkbenchSession(props: Props) {
  const { t } = useT("common");
  const [open, setOpen] = useState(false);
  const contentId = useId();
  return (
    <section className="border-t border-border pt-2">
      <Button variant="ghost" size="sm" className="w-full justify-start"
        aria-expanded={open} aria-controls={contentId} onClick={() => setOpen(!open)}>
        <Folder />
        {t(($) => $.workbench.files.management)}
        <ChevronRight className={open ? "ml-auto rotate-90" : "ml-auto"} />
      </Button>
      <div id={contentId}>
        {open && <WorkbenchBody {...props} />}
      </div>
    </section>
  );
}

function WorkbenchBody({ wsId, context, hasProject = true, projectId }: Props) {
  const scope = useId();
  const { t } = useT("common");
  const enabled = useFeatureEnabled(PLUGINS_V1_FLAG, false);
  const installed = useQuery({ ...pluginInstallationsOptions(wsId), enabled: enabled && Boolean(wsId), refetchInterval: 5_000, refetchIntervalInBackground: false, refetchOnWindowFocus: "always", refetchOnMount: "always", retry: false,
  });
  const panels = enabled && !installed.isError ? sidebarPanels(installed.data?.plugins ?? [], isDesktopShell() ? "desktop" : "web",
        ) : [];
  const tabs: WorkbenchTab[] = [
    { id: "files", label: t(($) => $.workbench.files.title) },
    { id: "diagnostics", label: t(($) => $.workbench.diagnostics) }, ...panels,
  ];
  const [selected, setSelected] = useState("files");
  const active = tabs.some((tab) => tab.id === selected) ? selected : "files";
  const panel = panels.find((entry) => entry.id === active);
  const ws = useWS();
  const queryClient = useQueryClient();
  const { send, subscribe, onReady } = ws;
  useEffect(() => {
    let mounted = true;
    const refresh = async () => {
      const queryKey = ["workspaces", wsId, "workbench", scope];
      await queryClient.cancelQueries({ queryKey });
      if (mounted) await queryClient.invalidateQueries({ queryKey });
    };
    void refresh();
    const unsubscribe = onReady(() => { void refresh(); });
    return () => { mounted = false; unsubscribe(); };
  }, [send, subscribe, onReady, wsId, scope, queryClient]);
  return (
    <Tabs value={active} onValueChange={(value) => setSelected(String(value))} className="min-h-0 flex-1 gap-2 px-1 py-2">
    <div className="shrink-0 overflow-x-auto overflow-y-hidden">
      <TabsList aria-label={t(($) => $.workbench.files.management)} className="flex w-max max-w-none justify-start">{tabs.map((tab) => (
          <TabsTrigger key={tab.id} value={tab.id} className="shrink-0 whitespace-nowrap">{tab.label}</TabsTrigger>))}</TabsList>
    </div>
    <TabsContent key={active} value={active} className="min-h-0 overflow-y-auto text-muted-foreground">
      {panel ? (
          <SidebarPanel wsId={wsId} context={context} panel={panel} />
        ) : active === "files" ? (
          <FilesTab wsId={wsId} context={context} hasProject={hasProject} projectId={projectId} />
        ) : (
          <WorkbenchResourceState wsId={wsId} scope={scope} context={context} hasProject={hasProject} />
        )}
    </TabsContent>
  </Tabs>
  );
}
