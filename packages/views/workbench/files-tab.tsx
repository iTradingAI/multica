"use client";

import { useEffect, useId, useMemo, useState } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useWS } from "@multica/core/realtime";
import {
  requestWorkspaceFiles,
  WorkspaceFilesError,
  type WorkbenchContext,
  type WorkspaceFilesTransport,
} from "@multica/core/workspace-files";
import { Button } from "@multica/ui/components/ui/button";
import { ChevronRight, File, Folder } from "lucide-react";
import { useT } from "../i18n";
import { FileSelectionPreview } from "./file-selection-preview";
import { IssueFileTouches } from "./issue-file-touches";

interface Props {
  wsId: string;
  context: WorkbenchContext;
  hasProject: boolean;
}

export function FilesTab(props: Props) {
  const { send, subscribe, onReady } = useWS();
  const ws = useMemo(() => ({ send, subscribe }), [send, subscribe]);
  const [epoch, setEpoch] = useState(0);
  useEffect(() => {
    const invalidate = () => setEpoch((value) => value + 1);
    const cleanups = [
      onReady(invalidate),
      ...(
        [
          "member:removed",
          "member:updated",
          "project:updated",
          "project:deleted",
          "project_resource:created",
          "project_resource:updated",
          "project_resource:deleted",
          "issue:updated",
        ] as const
      ).map((event) => subscribe(event, invalidate)),
    ];
    return () => cleanups.forEach((cleanup) => cleanup());
  }, [subscribe, onReady]);
  return <FilesSession key={epoch} {...props} ws={ws} />;
}

function FilesSession({
  wsId,
  context,
  hasProject,
  ws,
}: Props & { ws: WorkspaceFilesTransport }) {
  const scope = useId();
  const { t } = useT("common");
  const resources = useQuery({
    queryKey: [
      "workspaces",
      wsId,
      "workbench",
      scope,
      "files-resources",
      context,
    ],
    queryFn: ({ signal }) =>
      requestWorkspaceFiles(ws, { operation: "resources", context }, signal),
    enabled: hasProject && Boolean(wsId),
    retry: false,
    gcTime: 0,
  });
  const [resourceId, setResourceId] = useState("");
  const directories = resources.data?.resources ?? [];
  const active = directories.some(
    (resource) => resource.resource_id === resourceId,
  )
    ? resourceId
    : directories[0]?.resource_id;
  const touches = <IssueFileTouches wsId={wsId} ws={ws} context={context} />;
  if (!hasProject)
    return (
      <div>
        {touches}
        <p role="status">{t(($) => $.workbench.no_project)}</p>
      </div>
    );
  if (resources.isPending)
    return (
      <div>
        {touches}
        <p role="status">{t(($) => $.workbench.files.loading)}</p>
      </div>
    );
  if (resources.isError)
    return (
      <div>
        {touches}
        <FileError
          error={resources.error}
          retry={() => {
            void resources.refetch();
          }}
        />
      </div>
    );
  if (!active)
    return (
      <div>
        {touches}
        <p role="status">{t(($) => $.workbench.no_resources)}</p>
      </div>
    );
  return (
    <div className="space-y-3">
      {touches}
      <label className="block text-caption">
        {t(($) => $.workbench.files.resource)}
        <select
          className="mt-1 w-full rounded-md border bg-background p-2"
          value={active}
          onChange={(event) => setResourceId(event.target.value)}
        >
          {directories.map((resource, i) => (
            <option key={resource.resource_id} value={resource.resource_id}>
              {t(($) => $.workbench.directory, { number: i + 1 })}
            </option>
          ))}
        </select>
      </label>
      <FileBrowser
        key={active}
        wsId={wsId}
        scope={scope}
        ws={ws}
        context={context}
        resourceId={active}
      />
    </div>
  );
}

interface BrowserProps {
  wsId: string;
  scope: string;
  ws: WorkspaceFilesTransport;
  context: WorkbenchContext;
  resourceId: string;
}
function FileBrowser(props: BrowserProps) {
  const { t } = useT("common");
  const [selection, setSelection] = useState<{
    path: string;
    type: "regular" | "directory";
  }>({ path: ".", type: "directory" });
  const directory =
    selection.type === "directory"
      ? selection.path
      : selection.path.includes("/")
        ? selection.path.slice(0, selection.path.lastIndexOf("/"))
        : ".";
  const parts = directory === "." ? [] : directory.split("/");
  return (
    <div className="space-y-3">
      <nav
        aria-label={t(($) => $.workbench.files.breadcrumb)}
        className="flex flex-wrap items-center gap-1 text-caption"
      >
        <Button
          size="xs"
          variant="ghost"
          onClick={() => setSelection({ path: ".", type: "directory" })}
        >
          {t(($) => $.workbench.files.root)}
        </Button>
        {parts.map((part, i) => (
          <Button
            size="xs"
            variant="ghost"
            key={i}
            onClick={() =>
              setSelection({
                path: parts.slice(0, i + 1).join("/"),
                type: "directory",
              })
            }
          >
            {part}
          </Button>
        ))}
      </nav>
      <Directory
        key={directory}
        {...props}
        path={directory}
        select={setSelection}
      />
      {selection.type === "regular" && (
        <section
          aria-label={selection.path}
          className="space-y-2 border-t pt-3"
        >
          <p className="break-all font-medium text-caption">{selection.path}</p>
          <FileSelectionPreview
            key={selection.path}
            wsId={props.wsId}
            ws={props.ws}
            context={props.context}
            resourceId={props.resourceId}
            path={selection.path}
          />
        </section>
      )}
    </div>
  );
}

type DirectoryProps = BrowserProps & {
  path: string;
  select: (entry: { path: string; type: "regular" | "directory" }) => void;
};
function Directory(props: DirectoryProps) {
  const [generation, setGeneration] = useState(0);
  return (
    <DirectoryPage
      key={generation}
      {...props}
      refresh={() => {
        props.select({ path: props.path, type: "directory" });
        setGeneration((value) => value + 1);
      }}
    />
  );
}
function DirectoryPage({
  path,
  select,
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
                {entry.type === "directory" && (
                  <Button
                    size="icon-xs"
                    variant="ghost"
                    aria-label={`${t(($) => (open ? $.workbench.files.collapse : $.workbench.files.expand))} ${entry.name}`}
                    aria-expanded={open}
                    onClick={() =>
                      setExpanded((values) =>
                        open
                          ? values.filter((value) => value !== child)
                          : [...values, child],
                      )
                    }
                  >
                    <ChevronRight className={open ? "rotate-90" : ""} />
                  </Button>
                )}
                <Button
                  size="sm"
                  variant="ghost"
                  className="min-w-0 justify-start"
                  onClick={() => select({ path: child, type: entry.type })}
                >
                  {entry.type === "directory" ? <Folder /> : <File />}
                  <span className="truncate">{entry.name}</span>
                </Button>
              </div>
              {entry.type === "directory" && open && (
                <div className="ml-4 border-l pl-2">
                  <Directory {...props} path={child} select={select} />
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
