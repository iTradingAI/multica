import { z } from "zod";
import { parseWithFallback } from "../api/schema";
import {
  WorkspaceFilesError,
  type WorkbenchContext,
  type WorkspaceFilesTransport,
} from "./client";

export interface ViewerSelectionRequest {
  context: WorkbenchContext;
  resource_id: string;
  path: string;
  installation_id: string;
  version_id: string;
  surface_key: string;
  digest: string;
  platform: "web" | "desktop";
  mount_id: string;
  generation: number;
  binding_generation?: number;
}
const SelectionSchema = z
  .object({ client_req_id: z.string(), selection_id: z.string().uuid() })
  .strict();

/** Selection IDs never leave the trusted host. The abort signal owns both the
 * authorization reservation and the established idle session. */
export function selectFileViewer(
  ws: WorkspaceFilesTransport,
  request: ViewerSelectionRequest,
  signal: AbortSignal,
  invalidated: () => void,
): Promise<string> {
  if (signal.aborted)
    return Promise.reject(new DOMException("Aborted", "AbortError"));
  const id = crypto.randomUUID();
  return new Promise((resolve, reject) => {
    let live = true;
    let selected = false;
    const subscriptions: (() => void)[] = [];
    const timer = setTimeout(() => fail("timeout"), 10_000);
    function cleanup() {
      if (!live) return;
      live = false;
      clearTimeout(timer);
      subscriptions.forEach((unsubscribe) => unsubscribe());
      signal.removeEventListener("abort", abort);
      try {
        ws.send({
          type: "workspace_files.cancel",
          payload: { client_req_id: id },
        });
      } catch {
        /* disconnected */
      }
    }
    function fail(code: string) {
      if (!live) return;
      cleanup();
      if (selected) invalidated();
      else reject(new WorkspaceFilesError(code));
    }
    function abort() {
      const pending = live && !selected;
      cleanup();
      if (pending) reject(new DOMException("Aborted", "AbortError"));
    }
    const ours = (value: unknown) =>
      z.object({ client_req_id: z.literal(id) }).safeParse(value).success;
    subscriptions.push(
      ws.subscribe("workspace_files.error", (value) => {
        if (!ours(value)) return;
        const result = z.object({ code: z.string() }).safeParse(value);
        fail(result.success ? result.data.code : "unavailable");
      }),
    );
    subscriptions.push(
      ws.subscribe("workspace_files.viewer_select_result", (value) => {
        if (!live || selected || !ours(value)) return;
        const result = parseWithFallback(
          value,
          SelectionSchema,
          null as z.infer<typeof SelectionSchema> | null,
          { endpoint: "workspace_files.viewer_select_result" },
        );
        if (!result) return fail("unavailable");
        selected = true;
        clearTimeout(timer);
        resolve(result.selection_id);
      }),
    );
    signal.addEventListener("abort", abort, { once: true });
    try {
      ws.send({
        type: "workspace_files.viewer_select",
        payload: { client_req_id: id, ...request },
      });
    } catch {
      fail("unavailable");
    }
  });
}
