import type { JSONContent } from "@tiptap/core";
import { DocumentBudgetExceeded } from "./document-budget";

/**
 * Above this source size, ContentEditor parses markdown in chunks instead of in
 * one shot. `@tiptap/markdown` parses via `marked`, whose tokenizer is O(n²) in
 * document length (measured: 533KB plain text → 61.8s parse, while the following
 * ProseMirror setContent is only 40ms). Whole-document parse is the bottleneck;
 * Traces from a typical structured 22KB issue showed hundreds of milliseconds
 * in one parse, so keep the single-parse path only for genuinely small docs.
 */
export const MARKDOWN_CHUNK_THRESHOLD = 8_000;

// A smaller parse window matters because the tokenizer cost grows roughly with
// the square of each chunk's length. 4KB keeps common issue descriptions cheap
// while still avoiding hundreds of tiny ProseMirror documents.
export const MARKDOWN_CHUNK_SIZE = 4_000;

export interface MarkdownManagerLike {
  parse(markdown: string): JSONContent;
}

/**
 * Parse markdown into a ProseMirror JSON doc in chunks to dodge marked's O(n²).
 *
 * Splitting into k chunks and parsing each independently drops the cost to
 * O(n²/k) — marked only ever scans within one small chunk. Cuts happen only at
 * blank lines OUTSIDE fenced code blocks, so every chunk is a complete sequence
 * of block nodes; concatenating the per-chunk docs reproduces the same document
 * a single parse would have produced.
 *
 * Known limitation: a "loose" list (items separated by blank lines) straddling a
 * chunk boundary may render as two adjacent lists. Acceptable trade-off vs. a
 * minute-long freeze, and only reachable on documents past the threshold.
 */
export function parseMarkdownChunked(
  manager: MarkdownManagerLike,
  markdown: string,
  chunkSize = MARKDOWN_CHUNK_SIZE,
  budget?: { maxNodes: number; maxChunkCharacters: number },
): JSONContent {
  const lines = markdown.split("\n");
  const chunks: string[] = [];
  let current: string[] = [];
  let currentLen = 0;
  let inFence = false;

  for (const line of lines) {
    // Track fenced code blocks so a cut never lands inside one.
    if (/^\s*(```|~~~)/.test(line)) inFence = !inFence;
    current.push(line);
    currentLen += line.length + 1;

    // Cut only at a paragraph boundary (blank line) outside a fence, once the
    // accumulated chunk is large enough.
    if (currentLen >= chunkSize && !inFence && line.trim() === "") {
      chunks.push(current.join("\n"));
      current = [];
      currentLen = 0;
    }
  }
  if (current.length) chunks.push(current.join("\n"));

  const merged: JSONContent = { type: "doc", content: [] };
  let nodeCount = 1;
  for (const chunk of chunks) {
    // An unsplittable paragraph/fence must not defeat the tokenizer budget.
    if (budget && chunk.length > budget.maxChunkCharacters) throw new DocumentBudgetExceeded();
    const doc = manager.parse(chunk);
    if (budget) {
      const pending = [doc.content ?? []];
      while (pending.length) {
        for (const node of pending.pop()!) {
          if (++nodeCount > budget.maxNodes) throw new DocumentBudgetExceeded();
          if (node.content) pending.push(node.content);
        }
      }
    }
    if (doc.content) {
      for (const node of doc.content) merged.content!.push(node);
    }
  }
  return merged;
}
