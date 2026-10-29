"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useFeatureEnabled } from "@multica/core/config";
import { PLUGINS_V1_FLAG } from "@multica/core/feature-flags";
import { pluginInstallationsOptions } from "@multica/core/plugins";
import {
  fileViewerMatch,
  selectFileViewer,
  requestWorkspaceFiles,
  type WorkbenchContext,
  type WorkspaceFilesTransport,
  type FileViewer,
} from "@multica/core/workspace-files";
import { isDesktopShell } from "../platform/local-directory";
import { PluginSurfaceFrame } from "../plugins/plugin-surface-frame";
import { FilePreview } from "./file-preview";

interface Props {
  wsId: string;
  ws: WorkspaceFilesTransport;
  context: WorkbenchContext;
  resourceId: string;
  path: string;
  bindingGeneration?: number;
}

export function FileSelectionPreview(props: Props) {
  const enabled = useFeatureEnabled(PLUGINS_V1_FLAG, false);
  const installations = useQuery({
    ...pluginInstallationsOptions(props.wsId),
    enabled,
    retry: false,
    refetchInterval: 5_000,
    refetchOnWindowFocus: "always",
  });
  const viewer =
    enabled && !installations.isError
      ? fileViewerMatch(
          installations.data?.plugins ?? [],
          props.path,
          isDesktopShell() ? "desktop" : "web",
        )
      : null;
  // Once a selected viewer fails, this choice falls back through a new user
  // read. No buffered viewer content is used by the builtin preview.
  const [failed, setFailed] = useState<string | null>(null);
  const key = viewer
    ? `${viewer.installation.id}:${viewer.installation.package_version_id}:${viewer.surface.key}`
    : "";
  const fail = useCallback(() => setFailed(key), [key]);
  if (!viewer || failed === key) return <FilePreview {...props} />;
  return <SelectedViewer key={key} {...props} viewer={viewer} fail={fail} />;
}

function SelectedViewer({
  wsId,
  ws,
  context,
  resourceId,
  path,
  bindingGeneration,
  viewer,
  fail,
}: Props & { viewer: FileViewer; fail: () => void }) {
  const lifecycle = useMemo(
    () => ({
      controller: new AbortController(),
      mountId: crypto.randomUUID(),
      selection: null as Promise<string> | null,
    }),
    [],
  );
  useEffect(() => {
    if (lifecycle.controller.signal.aborted) {
      lifecycle.controller = new AbortController();
      lifecycle.mountId = crypto.randomUUID();
      lifecycle.selection = null;
    }
    const controller = lifecycle.controller;
    return () => controller.abort();
  }, [lifecycle]);
  const read = useCallback(
    async (digest: string) => {
      const controller = lifecycle.controller;
      if (controller.signal.aborted)
        throw new DOMException("Aborted", "AbortError");
      lifecycle.selection ??= selectFileViewer(
        ws,
        {
          context,
          resource_id: resourceId,
          path,
          installation_id: viewer.installation.id,
          version_id: viewer.installation.package_version_id,
          surface_key: viewer.surface.key,
          digest,
          platform: isDesktopShell() ? "desktop" : "web",
          mount_id: lifecycle.mountId,
          generation: 1,
          ...(bindingGeneration
            ? { binding_generation: bindingGeneration }
            : {}),
        },
        lifecycle.controller.signal,
        fail,
      );
      const selectionId = await lifecycle.selection;
      const result = await requestWorkspaceFiles(
        ws,
        { operation: "viewer_read", context, resourceId, path, selectionId },
        controller.signal,
      );
      if (controller.signal.aborted)
        throw new DOMException("Aborted", "AbortError");
      return { text: result.text ?? "" };
    },
    [
      context,
      resourceId,
      path,
      bindingGeneration,
      viewer.installation.id,
      viewer.installation.package_version_id,
      viewer.surface.key,
      ws,
      lifecycle,
      fail,
    ],
  );
  return (
    <PluginSurfaceFrame
      wsId={wsId}
      installation={viewer.installation}
      surface={viewer.surface}
      issueId={context.kind === "issue" ? context.issue_id : undefined}
      readSelected={read}
      onFailure={fail}
    />
  );
}
