import { useQuery } from "@tanstack/react-query";
import { useCurrentWorkspace } from "@multica/core/paths";
import { runtimeListOptions } from "@multica/core/runtimes";
import type { AgentRuntime } from "@multica/core/types";

/**
 * Declared by daemons that can host pty sessions (server/pkg/protocol/
 * terminal.go: DaemonCapabilityTerminalV1). Only runtimes advertising it are
 * offered as remote terminal targets.
 */
export const TERMINAL_CAPABILITY = "terminal-v1";

export interface TerminalMachine {
  id: string;
  label: string;
}

/**
 * Whether a runtime's declared metadata capabilities include the terminal
 * capability. Missing metadata (older backends) means no.
 */
export function runtimeSupportsTerminal(
  runtime: Pick<AgentRuntime, "metadata">,
): boolean {
  const capabilities = runtime.metadata?.capabilities;
  return Array.isArray(capabilities) && capabilities.includes(TERMINAL_CAPABILITY);
}

/**
 * The machine host name behind a runtime. `device_info` reads
 * "<host> · <agent info>" (e.g. "VM-0-9-ubuntu · codex-cli 0.128.0"); the
 * host part identifies the physical machine across its per-agent runtimes.
 */
export function terminalMachineLabel(runtime: AgentRuntime): string {
  const info = runtime.device_info?.trim() ?? "";
  const host = info.split("·")[0]?.trim();
  if (host) return host;
  return runtime.custom_name?.trim() || runtime.name?.trim() || runtime.id;
}

/**
 * Local + terminal-capable remote MACHINES for the picker. One entry per
 * machine, not per runtime: runtimes are one per agent CLI on the same host,
 * and a terminal goes to the machine's daemon regardless of which runtime id
 * subscribed. Grouping key is daemon_id (the machine identity); the entry's
 * id is any online terminal-capable runtime on that machine and is used as
 * the subscription target.
 */
export function toTerminalMachines(runtimes: AgentRuntime[]): TerminalMachine[] {
  const byMachine = new Map<string, TerminalMachine>();
  for (const runtime of runtimes) {
    if (runtime.status !== "online") continue;
    if (!runtimeSupportsTerminal(runtime)) continue;
    const key = runtime.daemon_id ?? terminalMachineLabel(runtime);
    if (byMachine.has(key)) continue;
    byMachine.set(key, { id: runtime.id, label: terminalMachineLabel(runtime) });
  }
  return [...byMachine.values()];
}

/**
 * The machine list for the floating terminal's target picker. Reuses the
 * same cached runtime list query the rest of the desktop app uses.
 */
export function useTerminalMachines(): TerminalMachine[] {
  // Unlike useWorkspaceId, the raw hook returns null outside a workspace
  // route — the floating panel must tolerate that and just show 本机.
  const wsId = useCurrentWorkspace()?.id;
  const { data: runtimes = [] } = useQuery({
    ...runtimeListOptions(wsId ?? ""),
    enabled: !!wsId,
  });
  return toTerminalMachines(runtimes);
}
