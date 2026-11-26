"use client";

import { useEffect, useLayoutEffect, useRef, useState } from "react";
import {
  requestWorkspaceFiles,
  WorkspaceFilesError,
  WORKSPACE_FILES_EDIT_MAX_BYTES,
  type WorkbenchContext,
  type WorkspaceFilesEditVersion,
  type WorkspaceFilesTransport,
} from "@multica/core/workspace-files";
import { Button } from "@multica/ui/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@multica/ui/components/ui/dialog";
import { useT } from "../i18n";
import { FilePreview, FileText } from "./file-preview";

interface Props {
  ws: WorkspaceFilesTransport;
  context: WorkbenchContext;
  resourceId: string;
  path: string;
  ready: boolean;
  bindingChanged?: boolean;
  close: () => void;
}

interface TextVersion {
  text: string;
  version: WorkspaceFilesEditVersion;
}

export function WorkspaceFileEditor({ ws, context, resourceId, path, ready, bindingChanged, close }: Props) {
  const { t } = useT("common");
  const request = useRef<AbortController | null>(null);
  const [base, setBase] = useState<TextVersion>();
  const [latest, setLatest] = useState<TextVersion>();
  const [draft, setDraft] = useState("");
  const [activity, setActivity] = useState<"opening" | "saving">();
  const [code, setCode] = useState("");
  const [confirmClose, setConfirmClose] = useState(false);
  const dirty = Boolean(base && draft !== base.text);
  const tooLarge = new TextEncoder().encode(draft).length > WORKSPACE_FILES_EDIT_MAX_BYTES;
  const needsReload = ["conflict", "save_unconfirmed", "forbidden", "invalid_path", "daemon_offline", "unavailable"].includes(code);
  const canSend = ready && !bindingChanged;
  const additionalVersion = latest && base && latest.version.bindingGeneration !== base.version.bindingGeneration;

  useLayoutEffect(() => () => request.current?.abort(), []);
  // Retire the old request during the binding change commit, before a
  // queued response can settle against the draft that stays mounted.
  useLayoutEffect(() => {
    if (!canSend && request.current) {
      request.current.abort();
      request.current = null;
      setActivity(undefined);
      setCode(activity === "saving" ? "save_unconfirmed" : "daemon_offline");
    }
  }, [canSend, activity]);
  useEffect(() => {
    if (!dirty && activity !== "saving") return;
    function alertBeforeUnload(event: BeforeUnloadEvent) {
      event.preventDefault();
      event.returnValue = "";
    }
    function confirmRouteChange(event: Event) {
      if (!event.cancelable || !("destination" in event) || !("navigationType" in event)) return;
      const destination = event.destination as { sameDocument: boolean; url: string };
      if (destination.sameDocument && event.navigationType !== "replace" &&
          new URL(destination.url).pathname !== window.location.pathname &&
          !window.confirm(t(($) => $.workbench.files.discard_prompt))) {
        event.preventDefault();
      }
    }
    const navigation = "navigation" in window && window.navigation instanceof EventTarget
      ? window.navigation : undefined;
    window.addEventListener("beforeunload", alertBeforeUnload);
    navigation?.addEventListener("navigate", confirmRouteChange);
    return () => {
      window.removeEventListener("beforeunload", alertBeforeUnload);
      navigation?.removeEventListener("navigate", confirmRouteChange);
    };
  }, [dirty, activity, t]);

  function tryClose() {
    if (dirty || activity === "saving") {
      setConfirmClose(true);
      return;
    }
    close();
  }

  async function run(operation: "edit_open" | "save") {
    if (!canSend || request.current || (operation === "save" && (!base || !dirty || needsReload || tooLarge))) return;
    const controller = new AbortController();
    const submitted = draft;
    const previousCode = code;
    request.current = controller;
    setActivity(operation === "save" ? "saving" : "opening");
    setLatest(undefined);
    if (operation === "save" || !base) setCode("");
    const selection = { context, resourceId, path };
    try {
      const result = await requestWorkspaceFiles(ws, operation === "save" && base
        ? { operation, ...selection, text: submitted, editVersion: base.version }
        : { operation: "edit_open", ...selection }, controller.signal);
      if (controller.signal.aborted) return;
      if (!result.editVersion || result.text === undefined) {
        setCode(operation === "save" ? "save_unconfirmed" : "unavailable");
        return;
      }
      const current = { text: result.text, version: result.editVersion };
      if (operation === "save") {
        setBase(current);
        // The draft may already include new edits typed during the save.
        setCode("saved");
      } else if (base) {
        setLatest(current);
        setCode(previousCode);
      } else {
        setBase(current);
        setDraft(current.text);
        setCode("");
      }
    } catch (error) {
      if (controller.signal.aborted) return;
      const reason = error instanceof WorkspaceFilesError ? error.code : "unavailable";
      setCode(operation === "edit_open" && base && needsReload ? previousCode : reason);
    } finally {
      if (request.current === controller) {
        request.current = null;
        setActivity(undefined);
      }
    }
  }

  function message() {
    switch (tooLarge ? "too_large" : code) {
      case "": return "";
      case "saved": return t(($) => $.workbench.files.saved);
      case "conflict": return t(($) => $.workbench.files.conflict);
      case "save_unconfirmed": return t(($) => $.workbench.files.save_unconfirmed);
      case "read_only": return t(($) => $.workbench.files.read_only);
      case "too_large": return t(($) => $.workbench.files.edit_too_large);
      case "invalid_utf8":
      case "binary_content": return t(($) => $.workbench.files.binary);
      case "busy": return t(($) => $.workbench.files.file_busy);
      case "forbidden": return t(($) => $.workbench.forbidden);
      case "daemon_offline": return t(($) => $.workbench.daemon_offline);
      case "daemon_upgrade_required": return t(($) => $.workbench.daemon_upgrade_required);
      case "copy_failed": return t(($) => $.workbench.files.copy_failed);
      default: return t(($) => $.workbench.files.edit_unavailable);
    }
  }

  const status = message();
  return (
    <Dialog open onOpenChange={(open) => { if (!open) tryClose(); }}>
      <DialogContent className="sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle className="break-all">{path.split("/").at(-1)}</DialogTitle>
          <DialogDescription className="break-all">{path}</DialogDescription>
        </DialogHeader>
        {base ? (
          <textarea aria-label={t(($) => $.workbench.files.draft)} spellCheck={false} value={draft} readOnly={bindingChanged}
            onChange={(event) => setDraft(event.target.value)}
            className="min-h-56 max-h-[45vh] w-full resize-y rounded-md border bg-background p-3 font-mono text-sm"
          />
        ) : (
          <div className="max-h-[55vh] overflow-auto rounded-md border bg-background p-3">
            {canSend ? <FilePreview ws={ws} context={context} resourceId={resourceId} path={path} />
              : !bindingChanged && <p role="status">{t(($) => $.workbench.files.connecting)}</p>}
          </div>
        )}
        {latest && !bindingChanged && <section className="space-y-2">
          <p className="font-medium">{t(($) => $.workbench.files.disk_version)}</p>
          <div className="max-h-32 overflow-auto rounded-md border bg-background p-2">
            <FileText text={latest.text} path={path} />
          </div>
          {additionalVersion
            ? <p role="status">{t(($) => $.workbench.files.binding_changed)}</p>
            : <Button variant="outline" size="sm" onClick={() => {
              setBase(latest);
              setLatest(undefined);
              setCode("");
            }}>{t(($) => $.workbench.files.use_disk_version)}</Button>}
        </section>}
        {bindingChanged && <p role="status">{t(($) => $.workbench.files.binding_changed)}</p>}
        {status && <p role="status" className="text-caption text-muted-foreground">{status}</p>}
        {confirmClose && <div className="flex flex-wrap items-center gap-2 rounded-md border p-2">
          <span className="mr-auto text-caption">{t(($) => $.workbench.files.discard_prompt)}</span>
          <Button size="sm" variant="outline" onClick={() => setConfirmClose(false)}>{t(($) => $.workbench.files.keep_editing)}</Button>
          <Button size="sm" variant="destructive" onClick={close}>{t(($) => $.workbench.files.discard)}</Button>
        </div>}
        <DialogFooter>
          {base ? (
            <>
              <Button variant="ghost" onClick={() => {
                const onCopyFailed = () => { if (!needsReload) setCode("copy_failed"); };
                if (navigator.clipboard) void navigator.clipboard.writeText(draft).catch(onCopyFailed);
                else onCopyFailed();
              }}>{t(($) => $.workbench.files.copy_draft)}</Button>
              {needsReload && <Button variant="outline" disabled={!canSend || Boolean(activity)} onClick={() => void run("edit_open")}>
                {t(($) => $.workbench.files.reload_disk)}
              </Button>}
              <Button disabled={!canSend || !dirty || tooLarge || needsReload || Boolean(activity)} onClick={() => void run("save")}>
                {activity === "saving" ? t(($) => $.workbench.files.saving) : t(($) => $.workbench.files.save)}
              </Button>
            </>
          ) : (
            <Button variant="outline" disabled={!canSend || Boolean(activity) ||
              ["read_only", "too_large", "binary_content", "invalid_utf8", "forbidden", "daemon_upgrade_required"].includes(code)}
              onClick={() => void run("edit_open")}>
              {activity === "opening" ? t(($) => $.workbench.files.loading) : t(($) => $.workbench.files.edit)}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
