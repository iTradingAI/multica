import { z } from "zod";
import { parseWithFallback } from "../api/schema";
import type { WSEventType, WSMessage } from "../types/events";

export type WorkbenchContext =
  | { kind: "issue"; issue_id: string; project_id?: never }
  | { kind: "project"; project_id: string; issue_id?: never };

export interface WorkspaceFilesTransport {
  send(message: WSMessage): void;
  subscribe(event: WSEventType, handler: (payload: unknown) => void,
  ): () => void;
}
export interface WorkspaceFilesResource { resource_id: string; display_name: string; access: "read_only";
}
export interface WorkspaceFilesResult {
  resources?: WorkspaceFilesResource[];
  entries?: { name: string; type: "regular" | "directory" }[];
  text?: string;
  nextCursor?: string;
  skipped?: number;
}
export class WorkspaceFilesError extends Error {
  constructor(readonly code: string) { super(code); this.name = "WorkspaceFilesError"; }
}
const ContextSchema = z.union([
  z.object({ kind: z.literal("issue"), issue_id: z.string().min(1) }).strict(),
  z.object({ kind: z.literal("project"), project_id: z.string().min(1) }).strict(),
]);
const ResourceSchema = z.object({ resource_id: z.string().min(1), display_name: z.string(), access: z.literal("read_only"),
});
const ResourcesSchema = z.object({ client_req_id: z.string(), context: ContextSchema, resources: z.array(ResourceSchema).max(512),
});
const EntryNameSchema = z
  .string()
  .min(1)
  .max(4096)
  .refine(
    (name) => name !== "." && name !== ".." && !/[\/\\:\u0000]/.test(name),
  );
const ListSchema = z.object({ client_req_id: z.string(), resource_id: z.string(), seq: z.number().int().nonnegative(), entries: z.array(z.object({ name: EntryNameSchema, type: z.enum(["regular", "directory"]),
      }),
    ).max(200),
  next_cursor: z.string().max(4096).optional(),
  skipped: z.number().int().nonnegative().optional(), final: z.boolean(),
});
const ReadSchema = z.object({ client_req_id: z.string(), resource_id: z.string(), seq: z.number().int().nonnegative(), data: z.string(), eof: z.boolean(),
});
const ErrorSchema = z.object({ client_req_id: z.string(), code: z.string() });
type Request = { context: WorkbenchContext } & (
  | { operation: "resources" }
  | { operation: "list";
      resourceId: string;
      path: string;
      cursor?: string;
      pageSize?: number;
    }
  | {
      operation: "read"; resourceId: string; path: string;
      bindingGeneration?: number;
    }
  | {
      operation: "viewer_read";
      resourceId: string;
      path: string;
      selectionId: string;
    }
);

/** One socket-scoped request. Unmount aborts it; terminal cleanup drops late frames. */
export function requestWorkspaceFiles(ws: WorkspaceFilesTransport, request: Request, signal?: AbortSignal,
): Promise<WorkspaceFilesResult> {
  if (signal?.aborted) return Promise.reject(new DOMException("Aborted", "AbortError"));
  if (!ContextSchema.safeParse(request.context).success) return Promise.reject(new WorkspaceFilesError("forbidden"));
  if (
    request.operation === "list" &&
    request.pageSize !== undefined &&
    (!Number.isInteger(request.pageSize) ||
      request.pageSize < 1 ||
      request.pageSize > 200)
  )
    return Promise.reject(new WorkspaceFilesError("unavailable"));
  const id = crypto.randomUUID();
  const event = `workspace_files.${request.operation}` as WSEventType;
  const responseEvent =
    request.operation === "resources"
      ? "workspace_files.resources_result"
      : request.operation === "list"
        ? "workspace_files.list_result"
        : "workspace_files.read_chunk";
  return new Promise((resolve, reject) => {
    const subscriptions: (() => void)[] = [];
    let finished = false;
    let seq = 0;
    let text = "";
    let bytes = 0;
    const entries: NonNullable<WorkspaceFilesResult["entries"]> = [];
    const timer = setTimeout(() => fail("timeout"), 10_000);
    function cleanup() {
      finished = true;
      text = "";
      clearTimeout(timer);
      subscriptions.forEach((unsubscribe) => unsubscribe());
      signal?.removeEventListener("abort", abort);
    }
    function cancel() {
      // A disconnected socket may already have retired the server request.
      try {
        ws.send({
          type: "workspace_files.cancel",
          payload: { client_req_id: id },
        });
      } catch {
        /* already disconnected */
      }
    }
    function fail(code: string) {
      if (finished) return;
      cleanup();
      cancel();
      reject(new WorkspaceFilesError(code));
    }
    function abort() {
      if (finished) return;
      cleanup();
      cancel();
      reject(new DOMException("Aborted", "AbortError"));
    }
    function complete(result: WorkspaceFilesResult) {
      if (finished) return;
      cleanup();
      resolve(result);
    }
    function ours(payload: unknown) {
      return z.object({ client_req_id: z.literal(id) }).safeParse(payload)
        .success;
    }
    subscriptions.push(
      ws.subscribe("workspace_files.error", (payload) => {
        if (!ours(payload)) return;
        const parsed = parseWithFallback(
          payload,
          ErrorSchema,
          null as z.infer<typeof ErrorSchema> | null,
          { endpoint: "workspace_files.error" },
        );
        fail(parsed?.code ?? "unavailable");
      }),
    );
    subscriptions.push(
      ws.subscribe(responseEvent, (payload) => {
        if (finished || !ours(payload)) return;
        if (request.operation === "resources") {
          const parsed = parseWithFallback(
            payload,
            ResourcesSchema,
            null as z.infer<typeof ResourcesSchema> | null,
            { endpoint: responseEvent },
          );
          if (
            !parsed ||
            parsed.context.kind !== request.context.kind ||
            (parsed.context.kind === "issue"
              ? parsed.context.issue_id !== request.context.issue_id
              : parsed.context.project_id !== request.context.project_id)
          )
            return fail("unavailable");
          complete({ resources: parsed.resources });
        } else if (request.operation === "list") {
          const parsed = parseWithFallback(
            payload,
            ListSchema,
            null as z.infer<typeof ListSchema> | null,
            { endpoint: responseEvent },
          );
          if (
            !parsed ||
            parsed.resource_id !== request.resourceId ||
            parsed.seq !== seq++ ||
            entries.length + parsed.entries.length > 200
          )
            return fail("unavailable");
          entries.push(...parsed.entries);
          if (!parsed.final && parsed.next_cursor) return fail("unavailable");
          if (parsed.final)
            complete({
              entries,
              nextCursor: parsed.next_cursor,
              skipped: parsed.skipped,
            });
        } else {
          const parsed = parseWithFallback(
            payload,
            ReadSchema,
            null as z.infer<typeof ReadSchema> | null,
            { endpoint: responseEvent },
          );
          if (
            !parsed ||
            parsed.resource_id !== request.resourceId ||
            parsed.seq !== seq++ ||
            seq > 33
          )
            return fail("unavailable");
          try {
            const data = Uint8Array.from(atob(parsed.data), (char) =>
              char.charCodeAt(0),
            );
            if (data.length > 32 * 1024) return fail("unavailable");
            bytes += data.length;
            if (bytes > 1024 * 1024) return fail("too_large");
            const chunk = new TextDecoder("utf-8", { fatal: true }).decode(
              data,
            );
            if (/[\u0000-\u0008\u000B\u000C\u000E-\u001F]/.test(chunk))
              return fail("binary_content");
            text += chunk;
          } catch {
            return fail("invalid_utf8");
          }
          if (parsed.eof) complete({ text });
        }
      }),
    );
    signal?.addEventListener("abort", abort, { once: true });
    try {
      ws.send({
        type: event,
        payload:
          request.operation === "viewer_read"
            ? { client_req_id: id, selection_id: request.selectionId }
            : {
                client_req_id: id,
                context: request.context,
                ...(request.operation === "resources"
                  ? {}
                  : {
                      resource_id: request.resourceId,
                      path: request.path,
                      ...(request.operation === "read" &&
                      request.bindingGeneration
                        ? { binding_generation: request.bindingGeneration }
                        : {}),
                      ...(request.operation === "list"
                        ? {
                            page_size: request.pageSize ?? 100,
                            ...(request.cursor
                              ? { cursor: request.cursor }
                              : {}),
                          }
                        : {}),
                    }),
              },
      });
    } catch {
      fail("unavailable");
    }
  });
}
