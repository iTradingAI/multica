export interface UpdaterPreferences {
  automaticUpdates: boolean;
}

export type UpdaterFailureReason = "unavailable" | "network" | "unknown";

export type UpdaterDownloadState =
  | { status: "idle" }
  | { status: "manual-checking" }
  | { status: "manual-current" }
  | { status: "manual-available"; version: string }
  | { status: "downloading"; version: string }
  | { status: "downloaded"; version: string }
  | { status: "error"; reason: UpdaterFailureReason };

export type ManualUpdateCheckResult =
  | {
      ok: true;
      currentVersion: string;
      latestVersion: string;
      available: boolean;
      manualInstall?: boolean;
      downloadState: UpdaterDownloadState;
    }
  | { ok: false; error: string; reason: UpdaterFailureReason };
