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
  it("prefers the host part of device info, falling back to name then id", () => {
    expect(terminalMachineLabel(runtime({ device_info: "win-box" }))).toBe("win-box");
    expect(
      terminalMachineLabel(
        runtime({ device_info: "VM-0-9-ubuntu · codex-cli 0.128.0" }),
      ),
    ).toBe("VM-0-9-ubuntu");
    expect(terminalMachineLabel(runtime({ device_info: " " }))).toBe("runtime");
    expect(
      terminalMachineLabel(runtime({ device_info: "", name: "", id: "rt-9" })),
    ).toBe("rt-9");
  });
});

describe("toTerminalMachines", () => {
  it("keeps only terminal-capable online runtimes in order", () => {
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

  it("shows one entry per machine, not per runtime", () => {
    const machines = toTerminalMachines([
      runtime({
        id: "rt-1",
        daemon_id: "daemon-A",
        device_info: "VM-0-9-ubuntu · codex-cli 0.128.0",
        metadata: { capabilities: ["terminal-v1"] },
      }),
      runtime({
        id: "rt-2",
        daemon_id: "daemon-A",
        device_info: "VM-0-9-ubuntu · OpenClaw 2026.9.3",
        metadata: { capabilities: ["terminal-v1"] },
      }),
      runtime({
        id: "rt-3",
        daemon_id: "daemon-B",
        device_info: "MaxPc · Claude Code",
        metadata: { capabilities: ["terminal-v1"] },
      }),
    ]);
    expect(machines).toEqual([
      { id: "rt-1", label: "VM-0-9-ubuntu" },
      { id: "rt-3", label: "MaxPc" },
    ]);
  });

  it("skips offline machines and falls back to the host as grouping key", () => {
    const machines = toTerminalMachines([
      runtime({
        id: "rt-1",
        daemon_id: null,
        device_info: "VM-0-9-ubuntu · codex-cli",
        status: "offline",
        metadata: { capabilities: ["terminal-v1"] },
      }),
      runtime({
        id: "rt-2",
        daemon_id: null,
        device_info: "VM-0-9-ubuntu · OpenClaw",
        metadata: { capabilities: ["terminal-v1"] },
      }),
      runtime({
        id: "rt-3",
        daemon_id: null,
        device_info: "VM-0-9-ubuntu · Claude Code",
        metadata: { capabilities: ["terminal-v1"] },
      }),
    ]);
    // rt-1 offline → machine still represented by another runtime; same host
    // groups them even without a daemon_id.
    expect(machines).toEqual([{ id: "rt-2", label: "VM-0-9-ubuntu" }]);
  });
});
