"use client";

import { forwardRef, useEffect, useMemo, useState } from "react";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../i18n";
import { ContentEditor, type ContentEditorProps, type ContentEditorRef } from "./content-editor";
import { descriptionPreview, exceedsDescriptionBudget } from "./utils/document-budget";

function LargeDescriptionPreview({ markdown }: { markdown: string }) {
  const { t } = useT("editor");
  const preview = useMemo(() => descriptionPreview(markdown), [markdown]);
  return (
    <div className="space-y-3" data-large-description-preview>
      <p className="text-muted-foreground text-body-sm">{t(($) => $.large_document.notice)}</p>
      <Button type="button" variant="outline" size="sm" onClick={() => {
        const url = URL.createObjectURL(new Blob([markdown], { type: "text/markdown;charset=utf-8" }));
        const link = document.createElement("a");
        link.href = url;
        link.download = "description.md";
        link.click();
        // Give the browser time to consume the URL before revoking it.
        setTimeout(() => URL.revokeObjectURL(url), 1_000);
      }}>{t(($) => $.large_document.download)}</Button>
      <pre className="max-h-96 overflow-auto whitespace-pre-wrap break-words text-body-sm" tabIndex={0}>
        {preview}
      </pre>
    </div>
  );
}

/** Issue descriptions opt into bounded loading; other composers keep their behavior. */
export const BoundedContentEditor = forwardRef<ContentEditorRef, ContentEditorProps & {
  onReadOnlyChange?: (readOnly: boolean) => void;
}>(
  function BoundedContentEditor({ onReadOnlyChange, ...props }, ref) {
    const [blocked, setBlocked] = useState<{ markdown: string; value: string | undefined } | null>(() => {
      const markdown = props.value ?? props.defaultValue ?? "";
      return exceedsDescriptionBudget(markdown) ? { markdown, value: props.value } : null;
    });

    // A cleaned-up description delivered by realtime updates can become editable
    // again. While editing, ContentEditor owns dirty/upload guards before it
    // checks incoming content and requests this read-only fallback.
    useEffect(() => {
      if (blocked === null || props.value === undefined || props.value === blocked.value) return;
      setBlocked(exceedsDescriptionBudget(props.value) ? { markdown: props.value, value: props.value } : null);
    }, [blocked, props.value]);

    const readOnly = blocked !== null;
    useEffect(() => { onReadOnlyChange?.(readOnly); }, [readOnly, onReadOnlyChange]);

    if (blocked !== null) return <LargeDescriptionPreview markdown={blocked.markdown} />;
    return <ContentEditor {...props} ref={ref} onDocumentLimitExceeded={(markdown) => {
      setBlocked({ markdown, value: props.value });
    }} />;
  },
);
