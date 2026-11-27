// @vitest-environment node
import { language, syntaxTree } from "@codemirror/language";
import { EditorState } from "@codemirror/state";
import { describe, expect, it } from "vitest";
import { loadFileEditorLanguage } from "./file-editor-language";

describe("file editor language", () => {
  it.each([
    ["src/client.MJS", "javascript"],
    ["src/parser.CTS", "typescript"],
    ["src/view.JSX", "javascript"],
    ["src/view.TsX", "typescript"],
    ["configuration.json", "json"],
    ["styles.css", "css"],
    ["index.htm", "html"],
    ["docs/readme.markdown", "markdown"],
    ["docker/compose.YML", "yaml"],
    ["script.py", "python"],
    ["main.go", "go"],
    ["lib.rs", "rust"],
    ["migration.sql", "sql"],
    ["setup.sh", "shell"],
  ])("loads the language for %s", async (path, expected) => {
    const extension = await loadFileEditorLanguage(path);
    expect(extension).toBeDefined();
    const state = EditorState.create({ extensions: extension });
    expect(state.facet(language)?.name).toBe(expected);
  });

  it.each([
    ["view.jsx", "const view = <h1>Title</h1>;"],
    ["view.tsx", "const title: string = 'Title'; const view = <h1>{title}</h1>;"],
  ])("parses JSX with the correct dialect in %s", async (path, doc) => {
    const state = EditorState.create({ doc, extensions: await loadFileEditorLanguage(path) });
    const tree = syntaxTree(state).toString();
    expect(tree).toContain("JSXElement");
    expect(tree).not.toContain("⚠");
  });

  it.each(["note.txt", "archive.unknown", "dir.ts/Makefile", "ts"])(
    "leaves %s in plain text",
    (path) => expect(loadFileEditorLanguage(path)).toBeUndefined(),
  );
});
