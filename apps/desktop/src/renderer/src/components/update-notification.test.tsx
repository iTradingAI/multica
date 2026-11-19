import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { UpdaterDownloadState } from "../../../shared/updater-types";
import { UpdateNotification } from "./update-notification";

const mocks = vi.hoisted(() => ({
  installUpdate: vi.fn(),
  openExternal: vi.fn(),
}));

describe("UpdateNotification", () => {
  let update: (state: UpdaterDownloadState) => void;

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
        getDownloadState: async () => ({ status: "idle" }),
        onDownloadState: (listener: typeof update) => {
          update = listener;
          return vi.fn();
        },
        installUpdate: mocks.installUpdate,
      },
    });
  });

  it("opens the downloaded version's changelog from the update prompt", () => {
    render(<UpdateNotification />);
    act(() => update({ status: "downloaded", version: "0.4.27" }));

    expect(screen.queryByRole("button", { name: "Later" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "See changelog" }));

    expect(mocks.openExternal).toHaveBeenCalledWith(
      "https://multica.ai/changelog#release-0-4-27",
    );
  });

  it("still installs the update immediately from the primary action", () => {
    render(<UpdateNotification />);
    act(() => update({ status: "downloaded", version: "0.4.27" }));

    fireEvent.click(screen.getByRole("button", { name: "Restart now" }));

    expect(mocks.installUpdate).toHaveBeenCalledOnce();
  });

  it("offers the macOS installer instead of a restart", () => {
    render(<UpdateNotification />);
    act(() => update({ status: "manual-available", version: "0.4.27" }));

    expect(
      screen.getByText("Download and install v0.4.27 to update Multica."),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Open download" }));

    expect(mocks.installUpdate).toHaveBeenCalledWith("0.4.27");
    act(() => update({ status: "manual-checking" }));
    expect(screen.queryByRole("button", { name: "Open download" })).not.toBeInTheDocument();
  });

  it("shows an error if the update action cannot start", async () => {
    mocks.installUpdate.mockRejectedValueOnce(new Error("blocked"));
    render(<UpdateNotification />);
    act(() => update({ status: "manual-available", version: "0.4.27" }));

    fireEvent.click(screen.getByRole("button", { name: "Open download" }));

    expect(
      await screen.findByText("Unable to open the update download."),
    ).toBeInTheDocument();
  });
});
