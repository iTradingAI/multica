import { SettingsPage } from "@multica/views/settings";
import { useT } from "@multica/views/i18n";
import { Download, Server } from "lucide-react";
import { DaemonSettingsTab } from "../components/daemon-settings-tab";
import { UpdatesSettingsTab } from "../components/updates-settings-tab";

/**
 * Wraps `SettingsPage` so the desktop-only extra tabs can pull their labels
 * from i18n. The route element has to be a component (not a literal JSX
 * value) for `useT` to run.
 */
export function DesktopSettingsRoute() {
  const { t } = useT("settings");
  return (
    <SettingsPage
      extraDeviceTabs={[
        {
          value: "daemon",
          label: t(($) => $.desktop.daemon.title),
          icon: Server,
          content: <DaemonSettingsTab />,
        },
        {
          value: "updates",
          label: t(($) => $.desktop.tabs.updates),
          icon: Download,
          content: <UpdatesSettingsTab />,
        },
      ]}
    />
  );
}
