"use client";

import { useMemo } from "react";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../i18n";
import { formatBytes } from "../format-bytes";
import { isOutputTruncated, type TimelineItem } from "./build-timeline";
import { ToolDetailSurface } from "./detail-surfaces";
import { redactSecrets } from "./redact";
import { base64ByteLength, readImageResult } from "./trace-event-presenter";
import { boundToolOutput } from "./tool-output-preview";

/** Shared by inline issue/inbox activity and the full transcript inspector. */
export function ToolOutputBody({ item }: { item: TimelineItem }) {
  const { t } = useT("agents");
  const preview = useMemo(() => boundToolOutput(item.output ?? ""), [item.output]);
  const clipped = item.outputPreviewClipped === true || preview.clipped;
  const image = useMemo(() => clipped ? null : readImageResult(item.output), [clipped, item.output]);
  const note = isOutputTruncated(item) ? t(($) => $.transcript.output_truncated_note) : undefined;

  return <div>
    {clipped && <div className="space-y-1 px-3 pt-2">
      <p className="text-micro text-muted-foreground">{t(($) => $.transcript.output_preview_clipped)}</p>
      <Button type="button" variant="outline" size="xs" onClick={() => {
        // Download the record, not the decoded/redacted preview. Transparent
        // endings preserve CRLF on Windows; no BOM or extra newline is added.
        const source = item.originalOutput ?? item.output ?? "";
        const url = URL.createObjectURL(new Blob([source], { type: "text/plain;charset=utf-8", endings: "transparent" }));
        const link = document.createElement("a");
        link.href = url;
        link.download = `tool-output-${item.seq}.txt`;
        link.click();
        setTimeout(() => URL.revokeObjectURL(url), 1_000);
      }}>{t(($) => $.transcript.download_output_record)}</Button>
    </div>}
    {image ? <figure className="px-2 py-1">
      <img src={`data:${image.mediaType};base64,${image.base64}`}
        alt={t(($) => $.transcript.image_result)} className="max-h-80 w-full rounded-md border object-contain" />
      <figcaption className="pt-1 text-micro text-faint-foreground">
        {t(($) => $.transcript.image_result)} · {formatBytes(base64ByteLength(image.base64))}
      </figcaption>
      {note && <span className="block pt-1 text-micro text-faint-foreground">{note}</span>}
    </figure> : <ToolDetailSurface text={redactSecrets(preview.text)} note={note} />}
  </div>;
}
