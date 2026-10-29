import { describe, expect, it } from "vitest";
import type { PluginInstallation } from "@multica/core/types";
import { sidebarPanels } from "./sidebar-panel";

export function installation(overrides: Partial<PluginInstallation> = {}): PluginInstallation {
  return {
    id: "installation-1", plugin_key: "example", name: "Example", version: "1.0.0", enabled: true,
    package_version_id: "version-1", granted_scopes: [], config: {}, config_schema: [], configured_secrets: [], hooks: [], resources: [], created_at: "", updated_at: "",
    surfaces: [{ key: "sidebar", type: "sidebar_panel", name: "Example sidebar", entry: "ui/main.js", platforms: [] }],
    ...overrides,
  };
}

describe("sidebar filtering", () => {
  for (const platform of ["web", "desktop"] as const) {
    for (const enabled of [true, false, undefined]) {
      for (const platforms of [[], ["web"], ["desktop"], ["web", "desktop"], ["other"]] as string[][]) {
        it(`${platform} enabled=${enabled} platforms=${platforms}`, () => {
          const fixture = installation({ enabled, surfaces: [{ key: "s", type: "sidebar_panel", name: "Sidebar", entry: "ui/main.js", platforms }] });
          expect(sidebarPanels([fixture], platform)).toHaveLength(enabled === true && (platforms.length === 0 || platforms.includes(platform)) ? 1 : 0);
        });
      }
    }
  }
  it("keeps separate contributions and excludes other surface types", () => {
    const fixture = installation();
    fixture.surfaces.push({ ...fixture.surfaces[0]!, key: "second" }, { ...fixture.surfaces[0]!, key: "issue", type: "issue_panel" });
    expect(sidebarPanels([fixture], "web").map((panel) => panel.id)).toEqual(["installation-1:sidebar", "installation-1:second"]);
  });
});
