// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { AgentRuntime } from "@multica/core/types";
import {
  runtimeSupportsTerminal,
  terminalMachineLabel,
  toTerminalMachines,
} from "./machines";

function runtime(overrides: Partial<AgentRuntime>): AgentRuntime {
  return {
    id: "rt-1",
    workspace_id: "ws-1",
    daemon_id: null,
    name: "runtime",
    custom_name: null,
    runtime_mode: "daemon",
    provider: "hermes",
    launch_header: "",
    status: "online",
    device_info: "max-mini",
    metadata: {},
    owner_id: null,
    visibility: "private",
    last_seen_at: null,
    created_at: "",
    updated_at: "",
    ...overrides,
  } as AgentRuntime;
}

describe("runtimeSupportsTerminal", () => {
  it("accepts only runtimes advertising terminal-v1 in metadata", () => {
    expect(
      runtimeSupportsTerminal(runtime({ metadata: { capabilities: ["terminal-v1"] } })),
    ).toBe(true);
    expect(
      runtimeSupportsTerminal(runtime({ metadata: { capabilities: ["other"] } })),
    ).toBe(false);
    expect(runtimeSupportsTerminal(runtime({ metadata: {} }))).toBe(false);
    // Older backends omit metadata entirely.
    expect(runtimeSupportsTerminal(runtime({ metadata: undefined }))).toBe(false);
  });
});

describe("terminalMachineLabel", () => {
  it("prefers device host info, falling back to the runtime id", () => {
    expect(terminalMachineLabel(runtime({ device_info: "win-box" }))).toBe("win-box");
    expect(terminalMachineLabel(runtime({ device_info: " " }))).toBe("runtime");
    expect(
      terminalMachineLabel(runtime({ device_info: "", name: "", id: "rt-9" })),
    ).toBe("rt-9");
  });
});

describe("toTerminalMachines", () => {
  it("keeps only terminal-capable runtimes in order", () => {
    const machines = toTerminalMachines([
      runtime({ id: "rt-1", metadata: { capabilities: ["terminal-v1"] } }),
      runtime({ id: "rt-2", device_info: "no-pty", metadata: { capabilities: [] } }),
      runtime({
        id: "rt-3",
        device_info: "linux-box",
        metadata: { capabilities: ["terminal-v1", "other"] },
      }),
    ]);
    expect(machines).toEqual([
      { id: "rt-1", label: "max-mini" },
      { id: "rt-3", label: "linux-box" },
    ]);
  });
});
