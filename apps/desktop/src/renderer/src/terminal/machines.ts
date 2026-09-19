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

/** Label by device host info, falling back to the runtime id. */
export function terminalMachineLabel(runtime: AgentRuntime): string {
  const host =
    runtime.device_info?.trim() || runtime.custom_name?.trim() || runtime.name?.trim();
  return host || runtime.id;
}

/** Local + terminal-capable remote machines, in picker order. */
export function toTerminalMachines(runtimes: AgentRuntime[]): TerminalMachine[] {
  return runtimes
    .filter(runtimeSupportsTerminal)
    .map((runtime) => ({ id: runtime.id, label: terminalMachineLabel(runtime) }));
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
