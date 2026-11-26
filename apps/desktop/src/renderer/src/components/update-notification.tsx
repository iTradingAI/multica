import { useEffect, useState } from "react";
import type { UpdaterDownloadState } from "../../../shared/updater-types";
import { AnimatePresence, motion } from "motion/react";
import { useReducedMotion } from "@multica/core/appearance";
import { RefreshCw, X } from "@multica/ui/icons";
import { UI_EASE_OUT, UI_MOTION_DURATION } from "@multica/ui/lib/motion";
import { useT } from "@multica/views/i18n";

// Non-macOS downloads run silently in the background. macOS instead exposes
// a manual installer because its unsigned builds cannot use Squirrel.Mac.
type UpdateState =
  | { status: "idle" }
  | { status: "ready"; version: string; manualInstall: boolean };

function changelogUrl(version: string): string {
  return `https://multica.ai/changelog#release-${version.replace(/\./g, "-")}`;
}

export function UpdateNotification() {
  const { t } = useT("settings");
  const prefersReducedMotion = useReducedMotion();
  const [state, setState] = useState<UpdateState>({ status: "idle" });
  const [dismissed, setDismissed] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);

  useEffect(() => {
    let receivedEvent = false;
    let mounted = true;
    const applyDownload = (download: UpdaterDownloadState) => {
      if (download.status === "manual-available" || download.status === "downloaded") {
        setState({ status: "ready", version: download.version, manualInstall: download.status === "manual-available" });
        setDismissed(false);
        setActionError(null);
      } else {
        setState({ status: "idle" });
      }
    };
    const cleanupState = window.updater.onDownloadState((download) => {
      receivedEvent = true;
      applyDownload(download);
    });
    void window.updater.getDownloadState().then((download) => {
      if (mounted && !receivedEvent) applyDownload(download);
    }).catch(() => {});
    return () => {
      mounted = false;
      cleanupState();
    };
  }, []);

  const visible = state.status === "ready" && !dismissed;

  return (
    <AnimatePresence>
      {visible && (
        <motion.div
          key="update-notification"
          initial={{ opacity: 0, y: prefersReducedMotion ? 0 : 8 }}
          animate={{ opacity: 1, y: 0 }}
          exit={{ opacity: 0, y: prefersReducedMotion ? 0 : 8 }}
          transition={{
            duration: prefersReducedMotion ? 0 : UI_MOTION_DURATION.standard,
            ease: UI_EASE_OUT,
          }}
          className="fixed bottom-4 right-4 z-50 w-80 rounded-lg border border-border bg-background p-4 shadow-lg"
        >
          <button
            type="button"
            onClick={() => setDismissed(true)}
            className="absolute top-2 right-2 rounded-md p-1 text-muted-foreground hover:text-foreground transition-colors"
            aria-label={t(($) => $.desktop.updates.banner_dismiss)}
          >
            <X className="size-3.5" />
          </button>

          <div className="flex items-start gap-3">
            <div className="mt-0.5 rounded-md bg-success/10 p-1.5">
              <RefreshCw className="size-4 text-success" />
            </div>
            <div className="flex-1 min-w-0">
              <p className="text-body font-medium">
                {t(($) => state.manualInstall ? $.desktop.updates.banner_manual_title : $.desktop.updates.banner_title)}
              </p>
              <p className="text-caption text-muted-foreground mt-0.5">
                {t(($) => state.manualInstall ? $.desktop.updates.banner_manual_body : $.desktop.updates.banner_body, { version: state.version })}
              </p>
              <div className="mt-2 flex items-center gap-1.5">
                <button
                  type="button"
                  onClick={() =>
                    window.desktopAPI.openExternal(changelogUrl(state.version))
                  }
                  className="inline-flex items-center rounded-md border border-border bg-background px-3 py-1.5 text-caption font-medium text-foreground hover:bg-accent transition-colors"
                >
                  {t(($) => $.desktop.updates.banner_changelog)}
                </button>
                <button
                  type="button"
                  onClick={() => {
                    void window.updater.installUpdate(state.manualInstall ? state.version : undefined).catch(() => {
                      setActionError(t(($) => state.manualInstall
                        ? $.desktop.updates.banner_open_failed
                        : $.desktop.updates.banner_install_failed));
                    });
                  }}
                  className="inline-flex items-center rounded-md bg-primary px-3 py-1.5 text-caption font-medium text-primary-foreground hover:bg-primary/90 transition-colors"
                >
                  {t(($) => state.manualInstall ? $.desktop.updates.open_download : $.desktop.updates.banner_restart)}
                </button>
              </div>
              {actionError && (
                <p className="mt-2 text-caption text-destructive">{actionError}</p>
              )}
            </div>
          </div>
        </motion.div>
      )}
    </AnimatePresence>
  );
}
