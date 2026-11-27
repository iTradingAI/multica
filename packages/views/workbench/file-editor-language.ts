import { StreamLanguage } from "@codemirror/language";
import type { Extension } from "@codemirror/state";

const loadJavaScript = (typescript: boolean, jsx: boolean) =>
  import("@codemirror/lang-javascript").then(({ javascript }) => javascript({ typescript, jsx }));

export function fileExtension(path: string) {
  return /\.([^.]+)$/.exec(path.split("/").at(-1) ?? "")?.[1]?.toLowerCase();
}

export function loadFileEditorLanguage(path: string): Promise<Extension> | undefined {
  switch (fileExtension(path)) {
    case "js":
    case "cjs":
    case "mjs": return loadJavaScript(false, false);
    case "jsx": return loadJavaScript(false, true);
    case "ts":
    case "cts":
    case "mts": return loadJavaScript(true, false);
    case "tsx": return loadJavaScript(true, true);
    case "json": return import("@codemirror/lang-json").then(({ json }) => json());
    case "css": return import("@codemirror/lang-css").then(({ css }) => css());
    case "html":
    case "htm": return import("@codemirror/lang-html").then(({ html }) => html());
    case "md":
    case "markdown": return import("@codemirror/lang-markdown").then(({ markdown }) => markdown());
    case "yaml":
    case "yml": return import("@codemirror/lang-yaml").then(({ yaml }) => yaml());
    case "py": return import("@codemirror/lang-python").then(({ python }) => python());
    case "go": return import("@codemirror/lang-go").then(({ go }) => go());
    case "rs": return import("@codemirror/lang-rust").then(({ rust }) => rust());
    case "sql": return import("@codemirror/lang-sql").then(({ sql }) => sql());
    case "sh":
    case "bash":
    case "zsh":
      return import("@codemirror/legacy-modes/mode/shell").then(({ shell }) => StreamLanguage.define(shell));
    default: return undefined;
  }
}
