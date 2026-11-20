import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { act } from "@testing-library/react";
import type { UpdaterDownloadState } from "../../../shared/updater-types";

const mocks = vi.hoisted(() => ({
  getPreferences: vi.fn(),
  setAutomaticUpdates: vi.fn(),
  checkForUpdates: vi.fn(),
  installUpdate: vi.fn(),
  getDownloadState: vi.fn(),
  unsubscribe: vi.fn(),
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
}));

const translations = {
  auto_save: { toast_saved: "Settings saved" },
  desktop: {
    updates: {
      title: "Updates",
      description: "Update preferences",
      current_version: "Current version",
      automatic_updates_title: "Automatic background updates",
      automatic_updates_description: "Download updates in the background",
      automatic_updates_description_macos: "Check for macOS updates",
      automatic_updates_save_failed: "Failed to save update settings",
      check_section_title: "Check for updates",
      check_section_description: "Check manually",
      check_section_description_macos: "Download macOS updates manually",
      up_to_date: "Up to date",
      downloading: "Downloading v{{version}}",
      manual_download: "Download v{{version}}",
      open_download: "Open download",
      check_now: "Check now",
      checking: "Checking",
      check_unavailable: "Unable to check right now. Try later.",
      check_network: "Check your connection and try again.",
      check_failed: "Unable to check. Please retry.",
      download_failed: "Download failed. You can keep using this version.",
      download_ready: "v{{version}} is ready to install.",
      retry: "Retry",
    },
  },
};

vi.mock("@multica/views/i18n", () => ({
  useT: () => ({
    t: (
      selector: (resources: typeof translations) => string,
      values?: Record<string, string>,
    ) => {
      const template = selector(translations);
      return Object.entries(values ?? {}).reduce(
        (result, [key, value]) => result.replace(`{{${key}}}`, value),
        template,
      );
    },
  }),
}));

vi.mock("sonner", () => ({
  toast: {
    success: mocks.toastSuccess,
    error: mocks.toastError,
  },
}));

import { UpdatesSettingsTab } from "./updates-settings-tab";

let downloadListener: (state: UpdaterDownloadState) => void;

describe("UpdatesSettingsTab", () => {
  beforeEach(() => {
    mocks.getPreferences.mockReset().mockResolvedValue({
      automaticUpdates: true,
    });
    mocks.setAutomaticUpdates.mockReset();
    mocks.checkForUpdates.mockReset();
    mocks.installUpdate.mockReset().mockResolvedValue(undefined);
    mocks.getDownloadState.mockReset().mockResolvedValue({ status: "idle" });
    mocks.unsubscribe.mockReset();
    mocks.toastSuccess.mockReset();
    mocks.toastError.mockReset();

    Object.defineProperty(window, "desktopAPI", {
      configurable: true,
      value: { appInfo: { version: "1.2.3" } },
    });
    Object.defineProperty(window, "updater", {
      configurable: true,
      value: {
        getPreferences: mocks.getPreferences,
        setAutomaticUpdates: mocks.setAutomaticUpdates,
        checkForUpdates: mocks.checkForUpdates,
        installUpdate: mocks.installUpdate,
        getDownloadState: mocks.getDownloadState,
        onDownloadState: (listener: typeof downloadListener) => {
          downloadListener = listener;
          return mocks.unsubscribe;
        },
      },
    });
  });

  it("replaces raw errors with a localized message and retries successfully", async () => {
    mocks.checkForUpdates.mockResolvedValueOnce({ ok: false, reason: "unavailable", error: "private HTTP headers and stack" });
    mocks.checkForUpdates.mockResolvedValueOnce({ ok: true, available: false, downloadState: { status: "idle" } });
    render(<UpdatesSettingsTab />);
    fireEvent.click(screen.getByRole("button", { name: "Check now" }));
    expect(await screen.findByRole("status")).toHaveTextContent("Unable to check right now");
    expect(screen.queryByText(/private HTTP/)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("Up to date")).toBeInTheDocument();
  });

  it("recovers when the check IPC itself rejects", async () => {
    mocks.checkForUpdates.mockRejectedValue(new Error("private IPC exception"));
    render(<UpdatesSettingsTab />);
    fireEvent.click(screen.getByRole("button", { name: "Check now" }));
    expect(await screen.findByRole("status")).toHaveTextContent("Unable to check. Please retry.");
    expect(screen.getByRole("button", { name: "Retry" })).toBeEnabled();
  });

  it("keeps a download error when metadata arrives later", async () => {
    let resolve!: (result: unknown) => void;
    mocks.checkForUpdates.mockImplementation(() => new Promise((done) => { resolve = done; }));
    render(<UpdatesSettingsTab />);
    fireEvent.click(screen.getByRole("button", { name: "Check now" }));
    act(() => {
      downloadListener({ status: "downloading", version: "1.2.4" });
      downloadListener({ status: "error", reason: "network" });
    });
    await act(async () => { resolve({ ok: true, available: true, latestVersion: "1.2.4", downloadState: { status: "downloading", version: "1.2.4" } }); });
    expect(screen.getByRole("status")).toHaveTextContent("Download failed");
    expect(screen.getByRole("button", { name: "Retry" })).toBeEnabled();
  });

  it("restores an in-progress download and shows completion", async () => {
    mocks.getDownloadState.mockResolvedValue({ status: "downloading", version: "1.2.4" });
    const { unmount } = render(<UpdatesSettingsTab />);
    expect(await screen.findByText("Downloading v1.2.4")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Check now" })).toBeDisabled();
    act(() => downloadListener({ status: "downloaded", version: "1.2.4" }));
    expect(screen.getByRole("status")).toHaveTextContent("v1.2.4 is ready to install.");
    expect(screen.queryByText("Downloading v1.2.4")).not.toBeInTheDocument();
    unmount();
    expect(mocks.unsubscribe).toHaveBeenCalledOnce();
  });

  it("keeps automatic failures quiet when no manual check is active", async () => {
    render(<UpdatesSettingsTab />);
    await waitFor(() => expect(mocks.getDownloadState).toHaveBeenCalled());
    act(() => downloadListener({ status: "error", reason: "network" }));
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it("loads the persisted preference and saves changes from the switch", async () => {
    mocks.getPreferences.mockResolvedValue({ automaticUpdates: false });
    mocks.setAutomaticUpdates.mockResolvedValue({ automaticUpdates: true });
    render(<UpdatesSettingsTab />);

    const toggle = screen.getByRole("switch", {
      name: "Automatic background updates",
    });
    // The switch renders as <span role="switch">, so jest-dom's toBeEnabled()
    // treats it as always enabled and does not actually wait for getPreferences
    // to resolve. Wait on the persisted value being reflected instead, which
    // deterministically holds until the loaded preference (false) is applied.
    await waitFor(() => expect(toggle).not.toBeChecked());

    fireEvent.click(toggle);

    await waitFor(() => {
      expect(mocks.setAutomaticUpdates).toHaveBeenCalledWith(true);
      expect(toggle).toBeChecked();
    });
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Settings saved", {
      id: "settings-auto-save",
    });
  });

  it("opens the macOS installer when a manual update is available", async () => {
    mocks.checkForUpdates.mockResolvedValue({
      ok: true,
      currentVersion: "1.2.3",
      latestVersion: "1.2.4",
      available: true,
      manualInstall: true,
      downloadState: { status: "manual-available", version: "1.2.4" },
    });
    Object.defineProperty(window, "desktopAPI", {
      configurable: true,
      value: { appInfo: { version: "1.2.3", os: "macos" } },
    });
    render(<UpdatesSettingsTab />);

    expect(screen.getByText("Check for macOS updates")).toBeInTheDocument();
    expect(screen.getByText("Download macOS updates manually")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Check now" }));

    expect(await screen.findByText("Download v1.2.4")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Open download" }));

    expect(mocks.installUpdate).toHaveBeenCalledWith("1.2.4");
    act(() => downloadListener({ status: "manual-current" }));
    expect(screen.queryByRole("button", { name: "Open download" })).not.toBeInTheDocument();
  });
});
