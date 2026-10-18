import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { UpdateNotification } from "./update-notification";

const mocks = vi.hoisted(() => ({
  installUpdate: vi.fn(),
  openExternal: vi.fn(),
}));

type UpdateDownloadedListener = (info: {
  version: string;
  releaseNotes?: string;
}) => void;
type UpdateAvailableListener = (info: {
  version: string;
  releaseNotes?: string;
  manualInstall?: boolean;
}) => void;

describe("UpdateNotification", () => {
  let updateDownloaded: UpdateDownloadedListener;
  let updateAvailable: UpdateAvailableListener;

  beforeEach(() => {
    mocks.installUpdate.mockReset().mockResolvedValue(undefined);
    mocks.openExternal.mockReset().mockResolvedValue(undefined);

    Object.defineProperty(window, "desktopAPI", {
      configurable: true,
      value: { openExternal: mocks.openExternal },
    });
    Object.defineProperty(window, "updater", {
      configurable: true,
      value: {
        onUpdateDownloaded: (listener: UpdateDownloadedListener) => {
          updateDownloaded = listener;
          return vi.fn();
        },
        onUpdateAvailable: (listener: UpdateAvailableListener) => {
          updateAvailable = listener;
          return vi.fn();
        },
        installUpdate: mocks.installUpdate,
      },
    });
  });

  it("opens the downloaded version's changelog from the update prompt", () => {
    render(<UpdateNotification />);
    act(() => updateDownloaded({ version: "0.4.27" }));

    expect(screen.queryByRole("button", { name: "Later" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "See changelog" }));

    expect(mocks.openExternal).toHaveBeenCalledWith(
      "https://multica.ai/changelog#release-0-4-27",
    );
  });

  it("still installs the update immediately from the primary action", () => {
    render(<UpdateNotification />);
    act(() => updateDownloaded({ version: "0.4.27" }));

    fireEvent.click(screen.getByRole("button", { name: "Restart now" }));

    expect(mocks.installUpdate).toHaveBeenCalledOnce();
  });

  it("offers the macOS installer instead of a restart", () => {
    render(<UpdateNotification />);
    act(() => updateAvailable({ version: "0.4.27", manualInstall: true }));

    expect(
      screen.getByText("Download and install v0.4.27 to update Multica."),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Open download" }));

    expect(mocks.installUpdate).toHaveBeenCalledOnce();
  });

  it("shows an error if the update action cannot start", async () => {
    mocks.installUpdate.mockRejectedValueOnce(new Error("blocked"));
    render(<UpdateNotification />);
    act(() => updateAvailable({ version: "0.4.27", manualInstall: true }));

    fireEvent.click(screen.getByRole("button", { name: "Open download" }));

    expect(
      await screen.findByText("Unable to open the update download."),
    ).toBeInTheDocument();
  });
});
