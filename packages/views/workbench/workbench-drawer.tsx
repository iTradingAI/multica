"use client";

import { useEffect, useId, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useFeatureEnabled } from "@multica/core/config";
import { PLUGINS_V1_FLAG } from "@multica/core/feature-flags";
import { pluginInstallationsOptions } from "@multica/core/plugins";
import { useWS } from "@multica/core/realtime";
import type { WorkbenchContext } from "@multica/core/workspace-files";
import { Button } from "@multica/ui/components/ui/button";
import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetTrigger,
} from "@multica/ui/components/ui/sheet";
import { Tabs, TabsList, TabsTrigger, TabsContent,
} from "@multica/ui/components/ui/tabs";
import { PanelRightOpen } from "lucide-react";
import { useT } from "../i18n";
import { isDesktopShell } from "../platform/local-directory";
import { SidebarPanel, sidebarPanels } from "./sidebar-panel";
import { WorkbenchResourceState } from "./workbench-resource-state";
import { FilesTab } from "./files-tab";

interface Props { wsId: string; context: WorkbenchContext; hasProject?: boolean;
}
interface WorkbenchTab { id: string; label: string;
}

export function WorkbenchDrawer(props: Props) {
  // Changing workspace or entry destroys the entire session, including the
  // open state, selected tab, in-flight queries, iframe and MessagePort.
  return (
    <WorkbenchSession key={JSON.stringify([props.wsId, props.context])} {...props} />
  );
}
function WorkbenchSession({ wsId, context, hasProject = true }: Props) {
  const { t } = useT("common");
  const [open, setOpen] = useState(false);
  return (
    <Sheet open={open} onOpenChange={setOpen}>
    <SheetTrigger render={<Button variant="ghost" size="icon-sm" aria-label={t(($) => $.workbench.title)} title={t(($) => $.workbench.title)} />}><PanelRightOpen /></SheetTrigger>
    <SheetContent side="right" className="w-full sm:max-w-lg overflow-hidden">
      <SheetHeader><SheetTitle>{t(($) => $.workbench.title)}</SheetTitle></SheetHeader>
      {open && (
          <WorkbenchBody wsId={wsId} context={context} hasProject={hasProject} />
        )}
    </SheetContent>
  </Sheet>
  );
}
function WorkbenchBody({ wsId, context, hasProject = true }: Props) {
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
  const { send, subscribe, onReconnect } = ws;
  useEffect(() => {
    let mounted = true;
    const refresh = async () => {
      const queryKey = ["workspaces", wsId, "workbench", scope];
      await queryClient.cancelQueries({ queryKey });
      if (mounted) await queryClient.invalidateQueries({ queryKey });
    };
    void refresh();
    const unsubscribe = onReconnect(() => { void refresh(); });
    return () => { mounted = false; unsubscribe(); };
  }, [send, subscribe, onReconnect, wsId, scope, queryClient]);
  return (
    <Tabs value={active} onValueChange={(value) => setSelected(String(value))} className="min-h-0 flex-1 px-4 pb-4">
    <TabsList aria-label={t(($) => $.workbench.title)} className="max-w-full shrink-0 overflow-x-auto justify-start">{tabs.map((tab) => (
          <TabsTrigger key={tab.id} value={tab.id}>{tab.label}</TabsTrigger>))}</TabsList>
    <TabsContent key={active} value={active} className="min-h-0 overflow-y-auto text-muted-foreground">
      {panel ? (
          <SidebarPanel wsId={wsId} context={context} panel={panel} />
        ) : active === "files" ? (
          <FilesTab wsId={wsId} context={context} hasProject={hasProject} />
        ) : (
          <WorkbenchResourceState wsId={wsId} scope={scope} context={context} hasProject={hasProject} />
        )}
    </TabsContent>
  </Tabs>
  );
}
