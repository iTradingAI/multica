// Bound both parsing work and the DOM that a description can create.
export const DESCRIPTION_BUDGET = {
  maxCharacters: 100_000,
  maxLines: 2_000,
  maxBlankLines: 500,
  maxNodes: 5_000,
  maxChunkCharacters: 16_000,
} as const;

export class DocumentBudgetExceeded extends Error {
  constructor() {
    super("Description exceeds the editor document budget");
    this.name = "DocumentBudgetExceeded";
  }
}

/** Scan without allocating an array proportional to the number of lines. */
export function exceedsDescriptionBudget(markdown: string): boolean {
  if (markdown.length > DESCRIPTION_BUDGET.maxCharacters) return true;
  let lines = 1;
  let blankLines = 0;
  let blank = true;
  for (let i = 0; i <= markdown.length; i++) {
    const char = markdown[i];
    if (i === markdown.length || char === "\r" || char === "\n") {
      if (blank && ++blankLines > DESCRIPTION_BUDGET.maxBlankLines) return true;
      if (char === "\r" && markdown[i + 1] === "\n") i++;
      if (i < markdown.length && ++lines > DESCRIPTION_BUDGET.maxLines) return true;
      blank = true;
    } else if (char?.trim() !== "") {
      blank = false;
    }
  }
  return false;
}

/** Plain text only: never send the collapsed/truncated preview to a parser or save. */
export function descriptionPreview(markdown: string): string {
  const maxCharacters = 12_000;
  const maxLines = 120;
  const lines: string[] = [];
  let length = 0;
  let start = 0;
  let previousBlank = false;
  for (let i = 0; i <= markdown.length; i++) {
    if (i !== markdown.length && markdown[i] !== "\r" && markdown[i] !== "\n") continue;
    const line = markdown.slice(start, Math.min(i, start + maxCharacters));
    const blank = line.trim() === "";
    if (!blank || !previousBlank) {
      lines.push(line.slice(0, maxCharacters - length));
      length += lines[lines.length - 1]!.length + 1;
      if (lines.length >= maxLines || length >= maxCharacters) break;
    }
    previousBlank = blank;
    if (markdown[i] === "\r" && markdown[i + 1] === "\n") i++;
    start = i + 1;
  }
  return lines.join("\n");
}
