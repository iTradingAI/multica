"use client";

import { useEffect, useState } from "react";
import ReactMarkdown from "react-markdown";
import rehypeSanitize from "rehype-sanitize";
import remarkGfm from "remark-gfm";
import { createLowlight, common } from "lowlight";
import type { Root, Element, Text } from "hast";
import {
  requestWorkspaceFiles,
  WorkspaceFilesError,
  type WorkbenchContext,
  type WorkspaceFilesTransport,
} from "@multica/core/workspace-files";
import { openExternal } from "../platform/open-external";
import { useT } from "../i18n";

const lowlight = createLowlight(common);

function highlighted(
  node: Root | Element | Text,
  key: number = 0,
): React.ReactNode {
  if (node.type === "text") return node.value;
  const children = node.children.map((child, i) =>
    highlighted(child as Element | Text, i),
  );
  // Only a fixed span vocabulary from lowlight becomes DOM. Source text is
  // always rendered as React text, never injected as HTML.
  return node.type === "root" ? (
    children
  ) : (
    <span
      key={key}
      className={
        Array.isArray(node.properties.className)
          ? node.properties.className.join(" ")
          : undefined
      }
    >
      {children}
    </span>
  );
}

export function FileText({ text, path }: { text: string; path: string }) {
  const suffix = path.split(".").at(-1)?.toLowerCase();
  if (suffix === "md" || suffix === "markdown")
    return (
      <div className="prose prose-sm dark:prose-invert max-w-none break-words">
        <ReactMarkdown
          skipHtml
          remarkPlugins={[remarkGfm]}
          rehypePlugins={[rehypeSanitize]}
          components={{
            // Files previews make no image requests, including relative assets.
            img: () => null,
            a: ({ href, children }) =>
              href && /^https?:\/\//i.test(href) ? (
                <a
                  href={href}
                  onClick={(event) => {
                    event.preventDefault();
                    openExternal(href);
                  }}
                >
                  {children}
                </a>
              ) : (
                <span>{children}</span>
              ),
          }}
        >
          {text}
        </ReactMarkdown>
      </div>
    );
  let code: React.ReactNode = text;
  if (suffix && lowlight.registered(suffix))
    code = highlighted(lowlight.highlight(suffix, text));
  return (
    <pre className="overflow-x-auto whitespace-pre text-caption">
      <code>{code}</code>
    </pre>
  );
}

export function FilePreview({
  ws,
  context,
  resourceId,
  path,
  bindingGeneration,
}: {
  ws: WorkspaceFilesTransport;
  context: WorkbenchContext;
  resourceId: string;
  path: string;
  bindingGeneration?: number;
}) {
  const { t } = useT("common");
  const [result, setResult] = useState<{ text?: string; code?: string }>({});
  useEffect(() => {
    const controller = new AbortController();
    let live = true;
    setResult({});
    void requestWorkspaceFiles(
      ws,
      { operation: "read", context, resourceId, path, bindingGeneration },
      controller.signal,
    ).then(
      (value) => {
        if (live) setResult({ text: value.text ?? "" });
      },
      (error: unknown) => {
        if (live)
          setResult({
            code:
              error instanceof WorkspaceFilesError ? error.code : "unavailable",
          });
      },
    );
    return () => {
      live = false;
      controller.abort();
    };
  }, [ws, context, resourceId, path, bindingGeneration]);
  if (result.code)
    return (
      <p role="status">
        {result.code === "too_large"
          ? t(($) => $.workbench.files.too_large)
          : result.code === "binary_content" || result.code === "invalid_utf8"
            ? t(($) => $.workbench.files.binary)
            : result.code === "forbidden"
              ? t(($) => $.workbench.forbidden)
              : t(($) => $.workbench.unavailable)}
      </p>
    );
  if (result.text === undefined)
    return <p role="status">{t(($) => $.workbench.files.loading)}</p>;
  return <FileText text={result.text} path={path} />;
}
