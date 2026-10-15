import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../analytics", () => ({ captureEvent: vi.fn() }));

// A controllable PerformanceObserver stand-in: records the callback so a test
// can fire synthetic long-task entries, and counts constructions so we can
// assert idempotent install.
type LongTask = {
  duration: number;
  name?: string;
  attribution?: Array<{ containerName: string; containerSrc: string }>;
};
let lastCallback: ((list: { getEntries: () => LongTask[] }) => void) | null;
let constructed: number;
let observeCalls: number;

class FakePerformanceObserver {
  constructor(cb: (list: { getEntries: () => LongTask[] }) => void) {
    constructed += 1;
    lastCallback = cb;
  }
  observe() {
    observeCalls += 1;
  }
}

function fireLongTask(duration: number, details: Omit<LongTask, "duration"> = {}) {
  lastCallback?.({ getEntries: () => [{ duration, ...details }] });
}

async function load() {
  vi.resetModules();
  const mod = await import("./freeze-watchdog");
  const { setDiagnosticRoute } = await import("./diagnostic-context");
  const { captureEvent } = await import("../analytics");
  return {
    installFreezeWatchdog: mod.installFreezeWatchdog,
    setDiagnosticRoute,
    captureEvent: captureEvent as unknown as ReturnType<typeof vi.fn>,
  };
}

beforeEach(() => {
  lastCallback = null;
  constructed = 0;
  observeCalls = 0;
  vi.stubGlobal("window", {});
  vi.stubGlobal("location", { pathname: "/acme/issues" });
  vi.stubGlobal("PerformanceObserver", FakePerformanceObserver);
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.clearAllMocks();
  vi.useRealTimers();
});

describe("installFreezeWatchdog", () => {
  it("reports the long-task name and each container attribution", async () => {
    const { installFreezeWatchdog, captureEvent } = await load();
    installFreezeWatchdog();
    const attribution = [
      { containerName: "preview", containerSrc: "about:blank" },
      { containerName: "", containerSrc: "" },
    ];
    fireLongTask(3000, { name: "same-origin-descendant", attribution });
    expect(captureEvent).toHaveBeenCalledWith("client_unresponsive", expect.objectContaining({
      name: "same-origin-descendant", attribution, duration_ms: 3000,
    }));
  });

  it("keeps empty attribution for top-level tasks", async () => {
    const { installFreezeWatchdog, captureEvent } = await load();
    installFreezeWatchdog();
    fireLongTask(3000, { name: "self", attribution: [] });
    expect(captureEvent).toHaveBeenCalledWith("client_unresponsive", expect.objectContaining({
      name: "self", attribution: [],
    }));
  });

  it("reports a long task at or above the 2s threshold with duration + path", async () => {
    const { installFreezeWatchdog, captureEvent } = await load();
    installFreezeWatchdog();

    fireLongTask(2300);

    expect(captureEvent).toHaveBeenCalledTimes(1);
    expect(captureEvent).toHaveBeenCalledWith("client_unresponsive", {
      source: "longtask",
      duration_ms: 2300,
      path: "/acme/issues",
      name: "",
      attribution: [],
    });
  });

  // Desktop runs a memory router, so `location.pathname` there is the packaged
  // index.html path and identifies no page at all. The shell publishes its real
  // route instead; web publishes nothing and keeps the URL.
  it("prefers the published diagnostic route over location.pathname", async () => {
    const { installFreezeWatchdog, setDiagnosticRoute, captureEvent } = await load();
    setDiagnosticRoute("/:slug/issues/:id");
    installFreezeWatchdog();

    fireLongTask(2300);

    expect(captureEvent).toHaveBeenCalledWith("client_unresponsive", {
      source: "longtask",
      duration_ms: 2300,
      path: "/:slug/issues/:id",
      name: "",
      attribution: [],
    });
  });

  it("falls back to location.pathname when no route was published (web)", async () => {
    const { installFreezeWatchdog, captureEvent } = await load();
    installFreezeWatchdog();

    fireLongTask(2300);

    expect(captureEvent).toHaveBeenCalledWith(
      "client_unresponsive",
      expect.objectContaining({ path: "/acme/issues" }),
    );
  });

  it("ignores blocks below the threshold (normal render cost)", async () => {
    const { installFreezeWatchdog, captureEvent } = await load();
    installFreezeWatchdog();

    fireLongTask(600);
    fireLongTask(1999);

    expect(captureEvent).not.toHaveBeenCalled();
  });

  it("is idempotent — a second install does not add a second observer", async () => {
    const { installFreezeWatchdog } = await load();
    installFreezeWatchdog();
    installFreezeWatchdog();

    expect(constructed).toBe(1);
    expect(observeCalls).toBe(1);
  });

  it("is a no-op on the server (no window)", async () => {
    vi.stubGlobal("window", undefined);
    const { installFreezeWatchdog, captureEvent } = await load();

    expect(() => installFreezeWatchdog()).not.toThrow();
    expect(constructed).toBe(0);
    expect(captureEvent).not.toHaveBeenCalled();
  });

  it("is a no-op when PerformanceObserver is unavailable", async () => {
    vi.stubGlobal("PerformanceObserver", undefined);
    const { installFreezeWatchdog } = await load();

    expect(() => installFreezeWatchdog()).not.toThrow();
  });

  it("emits at most one client_unresponsive per 60s cooldown window", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-01-01T00:00:00Z"));
    const { installFreezeWatchdog, captureEvent } = await load();
    installFreezeWatchdog();

    // A sustained freeze arrives as several long-task entries back to back.
    fireLongTask(2500);
    fireLongTask(2500);
    fireLongTask(3000);

    expect(captureEvent).toHaveBeenCalledTimes(1);
  });

  it("emits again only after the cooldown window elapses", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-01-01T00:00:00Z"));
    const { installFreezeWatchdog, captureEvent } = await load();
    installFreezeWatchdog();

    fireLongTask(2500);
    expect(captureEvent).toHaveBeenCalledTimes(1);

    // Still inside the window → suppressed.
    vi.advanceTimersByTime(59_999);
    fireLongTask(2500);
    expect(captureEvent).toHaveBeenCalledTimes(1);

    // Window elapsed → emits again.
    vi.advanceTimersByTime(1);
    fireLongTask(2500);
    expect(captureEvent).toHaveBeenCalledTimes(2);
  });
});
