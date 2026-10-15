import type { PluginInstallation, PluginSurface } from "../types/plugin";

export interface FileViewer {
  installation: PluginInstallation;
  surface: PluginSurface;
}

export function fileExtension(path: string): string {
  const name = path.slice(path.lastIndexOf("/") + 1);
  const dot = name.lastIndexOf(".");
  if (dot <= 0 || dot === name.length - 1) return "";
  const extension = name.slice(dot + 1);
  return /^[a-z0-9]+$/i.test(extension) ? extension.toLowerCase() : "";
}

export function fileViewerMatch(
  installations: PluginInstallation[],
  path: string,
  platform: "web" | "desktop",
): FileViewer | null {
  const extension = fileExtension(path);
  if (!extension) return null;
  // Grants are checked after counting candidates. An ungranted contender still
  // makes matching ambiguous; filtering it first would invent a winner.
  const candidates = installations
    .filter((installation) => installation.enabled === true)
    .flatMap((installation) =>
      installation.surfaces
        .filter(
          (surface) =>
            surface.type === "file_viewer" &&
            (!surface.platforms?.length ||
              surface.platforms.includes(platform)) &&
            surface.extensions?.includes(extension),
        )
        .map((surface) => ({ installation, surface })),
    );
  return candidates.length === 1 &&
    candidates[0]!.installation.granted_scopes.includes("files:read")
    ? candidates[0]!
    : null;
}
