"use client";

import { useEffect, useId, useState } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import type { FileTouchSelection } from "@multica/core/workspace-files";
import type {
  WorkbenchContext,
  WorkspaceFilesTransport,
} from "@multica/core/workspace-files";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../i18n";
import { FileSelectionPreview } from "./file-selection-preview";

export function IssueFileTouches({
  wsId,
  ws,
  context,
}: {
  wsId: string;
  ws: WorkspaceFilesTransport;
  context: WorkbenchContext;
}) {
  const { t } = useT("common");
  const scope = useId();
  const issueId = context.kind === "issue" ? context.issue_id : "";
  const [selected, setSelected] = useState<FileTouchSelection | null>(null);
  const [failure, setFailure] = useState(false);
  const [controller] = useState(() => ({ current: new AbortController() }));
  useEffect(() => {
    if (controller.current.signal.aborted)
      controller.current = new AbortController();
    return () => controller.current.abort();
  }, [controller]);
  const touches = useInfiniteQuery({
    queryKey: ["workspaces", wsId, "workbench", scope, "file-touches", issueId],
    enabled: Boolean(issueId),
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      api.listIssueFileTouches(issueId, pageParam, signal),
    getNextPageParam: (page) => page.next_cursor || undefined,
    retry: false,
    gcTime: 0,
    refetchInterval: 5_000,
  });
  if (!issueId) return null;
  const coverage = touches.data?.pages[0]?.coverage;
  return (
    <section className="space-y-2 border-b pb-3">
      <h3 className="text-caption font-medium">
        {t(($) => $.workbench.files.touches)}
      </h3>
      <p className="text-caption text-muted-foreground">
        {t(($) => $.workbench.files.touch_counts)}
      </p>
      {touches.isError && (
        <p role="status">{t(($) => $.workbench.unavailable)}</p>
      )}
      {touches.data?.pages
        .flatMap((page) => page.files)
        .map((file) => (
          <Button
            key={file.touch_id}
            size="sm"
            variant="ghost"
            className="w-full justify-between"
            onClick={() => {
              controller.current.abort();
              controller.current = new AbortController();
              const signal = controller.current.signal;
              setSelected(null);
              setFailure(false);
              void api
                .resolveIssueFileTouch(issueId, file.touch_id, signal)
                .then(
                  (selection) => {
                    if (!signal.aborted) setSelected(selection);
                  },
                  () => {
                    if (!signal.aborted) setFailure(true);
                  },
                );
            }}
          >
            <span className="truncate">{file.path}</span>
            <span>{file.call_count}</span>
          </Button>
        ))}
      {coverage && (
        <p className="text-caption text-muted-foreground">
          {t(($) => $.workbench.files.coverage, coverage)}
        </p>
      )}
      {touches.hasNextPage && (
        <Button
          size="sm"
          variant="outline"
          disabled={touches.isFetchingNextPage}
          onClick={() => {
            void touches.fetchNextPage();
          }}
        >
          {t(($) => $.workbench.files.more)}
        </Button>
      )}
      {failure && <p role="status">{t(($) => $.workbench.unavailable)}</p>}
      {selected && (
        <FileSelectionPreview
          key={`${selected.resource_id}:${selected.binding_generation}:${selected.path}`}
          wsId={wsId}
          ws={ws}
          context={context}
          resourceId={selected.resource_id}
          path={selected.path}
          bindingGeneration={selected.binding_generation}
        />
      )}
    </section>
  );
}
