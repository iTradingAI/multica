"use client";

import { useEffect, useId, useMemo, useState, useSyncExternalStore } from "react";
import { useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";
import { projectResourcesOptions } from "@multica/core/projects";
import { useWS } from "@multica/core/realtime";
import {
  requestWorkspaceFiles,
  WorkspaceFilesError,
  type WorkbenchContext,
  type WorkspaceFilesTransport,
} from "@multica/core/workspace-files";
import { Button } from "@multica/ui/components/ui/button";
import { ChevronRight, File, Folder } from "@multica/ui/icons";
import { useT } from "../i18n";
import { WorkspaceFileEditor } from "./workspace-file-editor";
import { IssueFileTouches } from "./issue-file-touches";

interface Props {
  wsId: string;
  context: WorkbenchContext;
  hasProject: boolean;
  projectId?: string;
}

export function FilesTab({ wsId, context, hasProject, projectId }: Props) {
  const { send, subscribe, subscribeStatus, getStatus } = useWS();
  const ws = useMemo(() => ({ send, subscribe }), [send, subscribe]);
  const status = useSyncExternalStore(subscribeStatus, getStatus, getStatus);
  const ready = status === "connected";
  const scope = useId();
  const { t } = useT("common");
  const queryClient = useQueryClient();
  const [resourceId, setResourceId] = useState("");
  const [file, setFile] = useState<{ resourceId: string; path: string; bindingChanged?: boolean }>();
  useEffect(() => {
    const refresh = () => void queryClient.invalidateQueries({
      queryKey: ["workspaces", wsId, "workbench", scope],
    });
    const invalidateFile = (payload: unknown) => {
      let id: unknown;
      if (payload && typeof payload === "object") {
        if ("resource_id" in payload) id = payload.resource_id;
        if ("resource" in payload && payload.resource && typeof payload.resource === "object" && "id" in payload.resource)
          id = payload.resource.id;
      }
      setFile((current) => current && (!id || id === current.resourceId)
        ? { ...current, bindingChanged: true } : current);
      refresh();
    };
    const cleanups = [
      subscribe("project_resource:created", refresh),
      subscribe("project_resource:updated", invalidateFile),
      subscribe("project_resource:deleted", invalidateFile),
      subscribe("member:removed", refresh),
      subscribe("member:updated", refresh),
      subscribe("project:updated", refresh),
      subscribe("project:deleted", refresh),
    ];
    return () => cleanups.forEach((cleanup) => cleanup());
  }, [subscribe, queryClient, wsId, scope]);

  const resources = useQuery({
    queryKey: ["workspaces", wsId, "workbench", scope, "files-resources", context],
    queryFn: ({ signal }) => requestWorkspaceFiles(ws, { operation: "resources", context }, signal),
    enabled: ready && hasProject && Boolean(wsId),
    retry: false,
    gcTime: 0,
  });
  const labelProjectId = context.kind === "project" ? context.project_id : projectId;
  const labels = useQuery({
    ...projectResourcesOptions(wsId, labelProjectId ?? ""),
    enabled: Boolean(labelProjectId && resources.data?.resources?.length),
  });
  const directories = resources.data?.resources ?? [];
  const active = directories.some((resource) => resource.resource_id === resourceId)
    ? resourceId : directories[0]?.resource_id;

  return (
    <div className="space-y-3">
      <IssueFileTouches wsId={wsId} ws={ws} context={context} />
      {!hasProject ? (
        <p role="status">{t(($) => $.workbench.no_project)}</p>
      ) : !ready ? (
        <p role="status">{t(($) => $.workbench.files.connecting)}</p>
      ) : resources.isError ? (
        <FileError error={resources.error} retry={() => void resources.refetch()} />
      ) : resources.isPending ? (
        <p role="status">{t(($) => $.workbench.files.loading)}</p>
      ) : !active ? (
        <p role="status">{t(($) => $.workbench.no_resources)}</p>
      ) : (
        <>
          <label className="block text-caption">
            {t(($) => $.workbench.files.resource)}
            <select className="mt-1 w-full truncate rounded-md border bg-background p-2"
              value={active} onChange={(event) => setResourceId(event.target.value)}>
              {directories.map((resource, i) => (
                <option key={resource.resource_id} value={resource.resource_id}>
                  {labels.data?.find((entry) => entry.id === resource.resource_id)?.label ||
                    t(($) => $.workbench.directory, { number: i + 1 })}
                </option>
              ))}
            </select>
          </label>
          <Directory key={active} wsId={wsId} scope={scope} ws={ws} context={context}
            resourceId={active} path="." openFile={(path) => setFile({ resourceId: active, path })} />
        </>
      )}
      {file && <WorkspaceFileEditor ws={ws} context={context} {...file} ready={ready}
        close={() => setFile(undefined)} />}
    </div>
  );
}

type DirectoryProps = {
  wsId: string;
  scope: string;
  ws: WorkspaceFilesTransport;
  context: WorkbenchContext;
  resourceId: string;
  path: string;
  openFile: (path: string) => void;
};
function Directory(props: DirectoryProps) {
  const [generation, setGeneration] = useState(0);
  return (
    <DirectoryPage
      key={generation}
      {...props}
      refresh={() => {
        setGeneration((value) => value + 1);
      }}
    />
  );
}
function DirectoryPage({
  path,
  openFile,
  refresh,
  ...props
}: DirectoryProps & { refresh: () => void }) {
  const { t } = useT("common");
  const pageScope = useId();
  const [expanded, setExpanded] = useState<string[]>([]);
  const listing = useInfiniteQuery({
    queryKey: [
      "workspaces",
      props.wsId,
      "workbench",
      props.scope,
      "directory",
      props.context,
      props.resourceId,
      path,
      pageScope,
    ],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      requestWorkspaceFiles(
        props.ws,
        {
          operation: "list",
          context: props.context,
          resourceId: props.resourceId,
          path,
          cursor: pageParam,
          pageSize: 100,
        },
        signal,
      ),
    getNextPageParam: (page) => page.nextCursor || undefined,
    retry: false,
    gcTime: 0,
  });
  // Retire every old page and nested request together on refresh. A fresh
  // instance starts at the first page rather than reusing invalid cursors.
  if (listing.isPending)
    return <p role="status">{t(($) => $.workbench.files.loading)}</p>;
  const error = listing.error;
  if (error instanceof WorkspaceFilesError && error.code === "invalid_cursor")
    return <FileError error={error} retry={refresh} />;
  return (
    <div className="space-y-1">
      <Button size="xs" variant="outline" onClick={refresh}>
        {t(($) => $.workbench.files.refresh)}
      </Button>
      {listing.isError && (
        <FileError
          error={error}
          retry={() => {
            void listing.refetch();
          }}
        />
      )}
      {listing.data?.pages
        .flatMap((page) => page.entries ?? [])
        .map((entry) => {
          const child = path === "." ? entry.name : `${path}/${entry.name}`;
          const open = expanded.includes(child);
          return (
            <div key={child}>
              <div className="flex items-center gap-1">
                <Button
                  size="sm"
                  variant="ghost"
                  className="min-w-0 justify-start"
                  aria-expanded={entry.type === "directory" ? open : undefined}
                  onClick={(event) => {
                    if (entry.type === "directory") {
                      setExpanded((values) => open
                        ? values.filter((value) => value !== child)
                        : [...values, child]);
                    } else if (event.detail === 0 || window.matchMedia("(pointer: coarse)").matches) {
                      openFile(child);
                    }
                  }}
                  onDoubleClick={entry.type === "regular" ? () => openFile(child) : undefined}
                >
                  {entry.type === "directory" ? (
                    <><ChevronRight className={open ? "rotate-90" : ""} /><Folder /></>
                  ) : <File />}
                  <span className="truncate">{entry.name}</span>
                </Button>
              </div>
              {entry.type === "directory" && open && (
                <div className="ml-4 border-l pl-2">
                  <Directory {...props} path={child} openFile={openFile} />
                </div>
              )}
            </div>
          );
        })}
      {listing.hasNextPage && (
        <Button
          size="sm"
          variant="outline"
          disabled={listing.isFetchingNextPage}
          onClick={() => {
            void listing.fetchNextPage();
          }}
        >
          {t(($) => $.workbench.files.more)}
        </Button>
      )}
      {listing.data?.pages.every((page) => !page.entries?.length) && (
        <p role="status">{t(($) => $.workbench.files.empty)}</p>
      )}
    </div>
  );
}

function FileError({
  error,
  retry,
}: {
  error: Error | null;
  retry: () => void;
}) {
  const { t } = useT("common");
  const code =
    error instanceof WorkspaceFilesError ? error.code : "unavailable";
  const message =
    code === "forbidden"
      ? t(($) => $.workbench.forbidden)
      : code === "daemon_offline"
        ? t(($) => $.workbench.daemon_offline)
        : code === "daemon_upgrade_required"
          ? t(($) => $.workbench.daemon_upgrade_required)
          : code === "timeout"
            ? t(($) => $.workbench.timeout)
            : t(($) => $.workbench.unavailable);
  return (
    <div className="space-y-2">
      <p role="status">{message}</p>
      <Button size="sm" variant="outline" onClick={retry}>
        {t(($) => $.workbench.retry)}
      </Button>
    </div>
  );
}
