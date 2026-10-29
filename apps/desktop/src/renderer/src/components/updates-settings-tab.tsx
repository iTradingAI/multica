import { useCallback, useEffect, useState } from "react";
import { AlertCircle, ArrowDownToLine, Check, Loader2 } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Switch } from "@multica/ui/components/ui/switch";
import { useT } from "@multica/views/i18n";
import { SettingsCard, SettingsRow, SettingsTab } from "@multica/views/settings";
import { toast } from "sonner";

type CheckState =
  | { status: "idle" }
  | { status: "checking" }
  | { status: "up-to-date" }
  | { status: "available"; latestVersion: string; manualInstall: boolean }
  | { status: "error"; message: string };

export function UpdatesSettingsTab() {
  const { t } = useT("settings");
  const [state, setState] = useState<CheckState>({ status: "idle" });
  const [automaticUpdates, setAutomaticUpdates] = useState(true);
  const [preferencesReady, setPreferencesReady] = useState(false);
  const [savingPreference, setSavingPreference] = useState(false);
  const currentVersion = window.desktopAPI.appInfo.version;
  const manualMacUpdates = window.desktopAPI.appInfo.os === "macos";

  useEffect(() => {
    let mounted = true;
    void window.updater
      .getPreferences()
      .then((preferences) => {
        if (mounted) setAutomaticUpdates(preferences.automaticUpdates);
      })
      .catch(() => {
        // The main process falls back to enabled when preferences cannot be
        // read. Keep the same safe default if IPC itself becomes unavailable.
      })
      .finally(() => {
        if (mounted) setPreferencesReady(true);
      });

    return () => {
      mounted = false;
    };
  }, []);

  const handleAutomaticUpdatesChange = useCallback(
    async (enabled: boolean) => {
      setSavingPreference(true);
      try {
        const preferences = await window.updater.setAutomaticUpdates(enabled);
        setAutomaticUpdates(preferences.automaticUpdates);
        toast.success(t(($) => $.auto_save.toast_saved), {
          id: "settings-auto-save",
        });
      } catch {
        toast.error(t(($) => $.desktop.updates.automatic_updates_save_failed));
      } finally {
        setSavingPreference(false);
      }
    },
    [t],
  );

  const handleCheck = useCallback(async () => {
    setState({ status: "checking" });
    const result = await window.updater.checkForUpdates();
    if (!result.ok) {
      setState({ status: "error", message: result.error });
      return;
    }
    setState(
      result.available
        ? {
            status: "available",
            latestVersion: result.latestVersion,
            manualInstall: result.manualInstall === true,
          }
        : { status: "up-to-date" },
    );
  }, []);

  const handleManualInstall = useCallback(async () => {
    try {
      await window.updater.installUpdate();
    } catch {
      setState({ status: "error", message: "Unable to open the update download." });
    }
  }, []);

  return (
    <SettingsTab
      title={t(($) => $.desktop.updates.title)}
    >
      <SettingsCard>
        <SettingsRow label={t(($) => $.desktop.updates.current_version)}>
          <span className="font-mono text-caption text-muted-foreground">
            v{currentVersion}
          </span>
        </SettingsRow>

        <SettingsRow
          label={t(($) => $.desktop.updates.automatic_updates_title)}
          description={
            manualMacUpdates
              ? t(($) => $.desktop.updates.automatic_updates_description_macos)
              : t(($) => $.desktop.updates.automatic_updates_description)
          }
        >
          <Switch
            checked={automaticUpdates}
            onCheckedChange={handleAutomaticUpdatesChange}
            disabled={!preferencesReady || savingPreference}
            aria-label={t(($) => $.desktop.updates.automatic_updates_title)}
          />
        </SettingsRow>

        <SettingsRow
          label={t(($) => $.desktop.updates.check_section_title)}
          align="start"
          description={
            <>
              <p>
                {manualMacUpdates
                  ? t(($) => $.desktop.updates.check_section_description_macos)
                  : t(($) => $.desktop.updates.check_section_description)}
              </p>
              {state.status === "up-to-date" && (
                <p className="mt-2 inline-flex items-center gap-1.5">
                  <Check className="size-3.5 text-success" />
                  {t(($) => $.desktop.updates.up_to_date)}
                </p>
              )}
              {state.status === "available" && (
                <div className="mt-2 flex items-center gap-2">
                  <p className="inline-flex items-center gap-1.5">
                    <ArrowDownToLine className="size-3.5 text-primary" />
                    {state.manualInstall
                      ? t(($) => $.desktop.updates.manual_download, {
                          version: state.latestVersion,
                        })
                      : t(($) => $.desktop.updates.downloading, {
                          version: state.latestVersion,
                        })}
                  </p>
                  {state.manualInstall && (
                    <Button variant="outline" size="sm" onClick={handleManualInstall}>
                      {t(($) => $.desktop.updates.open_download)}
                    </Button>
                  )}
                </div>
              )}
              {state.status === "error" && (
                <p className="mt-2 inline-flex items-center gap-1.5 text-destructive">
                  <AlertCircle className="size-3.5" />
                  {state.message}
                </p>
              )}
            </>
          }
        >
          <Button
            variant="outline"
            size="sm"
            onClick={handleCheck}
            disabled={state.status === "checking"}
          >
            {state.status === "checking" ? (
              <>
                <Loader2 className="size-3.5 animate-spin" />
                {t(($) => $.desktop.updates.checking)}
              </>
            ) : (
              t(($) => $.desktop.updates.check_now)
            )}
          </Button>
        </SettingsRow>
      </SettingsCard>
    </SettingsTab>
  );
}
