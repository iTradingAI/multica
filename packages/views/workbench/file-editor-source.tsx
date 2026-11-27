"use client";

import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { defaultKeymap, history, historyKeymap } from "@codemirror/commands";
import { HighlightStyle, syntaxHighlighting } from "@codemirror/language";
import { Annotation, Compartment, EditorState, Transaction } from "@codemirror/state";
import { EditorView, keymap } from "@codemirror/view";
import { tags } from "@lezer/highlight";
import { loadFileEditorLanguage } from "./file-editor-language";

const highlight = HighlightStyle.define([
  { tag: [tags.keyword, tags.tagName], color: "var(--file-syntax-keyword)" },
  { tag: [tags.string, tags.regexp], color: "var(--file-syntax-string)" },
  { tag: [tags.comment, tags.quote], color: "var(--muted-foreground)", fontStyle: "italic" },
  { tag: [tags.number, tags.bool, tags.null], color: "var(--file-syntax-number)" },
  { tag: [tags.propertyName, tags.attributeName], color: "var(--file-syntax-property)" },
  { tag: [tags.typeName, tags.className, tags.heading], color: "var(--file-syntax-type)" },
]);

const theme = EditorView.theme({
  "&": {
    "--file-syntax-keyword": "oklch(0.49 0.17 255)",
    "--file-syntax-string": "oklch(0.48 0.13 155)",
    "--file-syntax-number": "oklch(0.49 0.16 30)",
    "--file-syntax-property": "oklch(0.49 0.12 60)",
    "--file-syntax-type": "oklch(0.49 0.14 280)",
    maxHeight: "inherit",
    minHeight: "inherit",
    fontSize: "var(--text-body)",
    color: "var(--foreground)",
  },
  ".dark &": {
    "--file-syntax-keyword": "oklch(0.76 0.13 255)",
    "--file-syntax-string": "oklch(0.76 0.13 155)",
    "--file-syntax-number": "oklch(0.76 0.13 30)",
    "--file-syntax-property": "oklch(0.76 0.1 60)",
    "--file-syntax-type": "oklch(0.76 0.12 280)",
  },
  "&.cm-focused": { outline: "1px solid var(--ring)", outlineOffset: "-1px" },
  ".cm-scroller": { fontFamily: "var(--font-mono, ui-monospace, monospace)" },
  ".cm-content": { padding: "0.75rem 0", caretColor: "var(--foreground)" },
  ".cm-line": { padding: "0 0.75rem" },
});

const externalText = Annotation.define<boolean>();

interface Props {
  path: string;
  text: string;
  label: string;
  className?: string;
  readOnly?: boolean;
  onChange?: (text: string) => void;
}

export function FileEditorSource({ path, text, label, className, readOnly = false, onChange }: Props) {
  const host = useRef<HTMLDivElement>(null);
  const editor = useRef<EditorView>(null);
  const onChangeRef = useRef(onChange);
  const [config] = useState(() => ({ access: new Compartment(), language: new Compartment() }));

  useLayoutEffect(() => { onChangeRef.current = onChange; }, [onChange]);

  useLayoutEffect(() => {
    const view = new EditorView({
      parent: host.current!,
      extensions: [
        config.access.of([]),
        config.language.of([]),
        theme,
        syntaxHighlighting(highlight),
        EditorView.lineWrapping,
        history(),
        keymap.of([...defaultKeymap, ...historyKeymap]),
        EditorView.updateListener.of((update) => {
          if (update.docChanged && !update.transactions.some((tr) => tr.annotation(externalText))) {
            onChangeRef.current?.(update.state.sliceDoc());
          }
        }),
      ],
    });
    editor.current = view;
    return () => {
      view.destroy();
      editor.current = null;
    };
  }, [config]);

  useLayoutEffect(() => {
    editor.current?.dispatch({
      effects: config.access.reconfigure([
        EditorState.readOnly.of(readOnly),
        EditorView.editable.of(!readOnly),
        EditorView.contentAttributes.of({
          "aria-label": label,
          "aria-multiline": "true",
          "aria-readonly": String(readOnly),
          role: "textbox",
          tabindex: "0",
          spellcheck: "false",
        }),
      ]),
    });
  }, [config, label, readOnly]);

  useLayoutEffect(() => {
    const view = editor.current;
    if (!view) return;
    const doc = view.state.toText(text);
    if (doc.eq(view.state.doc)) return;
    view.dispatch({
      changes: { from: 0, to: view.state.doc.length, insert: doc },
      annotations: [externalText.of(true), Transaction.addToHistory.of(false)],
    });
  }, [text]);

  useEffect(() => {
    const view = editor.current;
    if (!view) return;
    let active = true;
    view.dispatch({ effects: config.language.reconfigure([]) });
    void loadFileEditorLanguage(path)?.then((extension) => {
      if (active) view.dispatch({ effects: config.language.reconfigure(extension) });
    }, () => {
      // A failed language chunk still leaves the source available as plain text.
    });
    return () => { active = false; };
  }, [config, path]);

  return <div ref={host} className={className} />;
}
