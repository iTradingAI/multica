// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import { parseMarkdownChunked } from "./parse-markdown-chunked";
import { DESCRIPTION_BUDGET, DocumentBudgetExceeded } from "./document-budget";

describe("parseMarkdownChunked", () => {
  it("caps total nested nodes across individually small chunks before returning a document", () => {
    const manager = { parse: vi.fn(() => ({ type: "doc", content: [
      { type: "bulletList", content: [{ type: "listItem", content: [{ type: "paragraph", content: [{ type: "text", text: "x" }] }] }] },
    ] })) };
    const budget = { maxNodes: 8, maxChunkCharacters: 100 };
    expect(() => parseMarkdownChunked(manager, "a\n\nb\n\nc\n\n", 1, budget)).toThrow(DocumentBudgetExceeded);
    expect(manager.parse).toHaveBeenCalledTimes(2);
  });

  it("rejects an unsplittable long block before calling the tokenizer", () => {
    const manager = { parse: vi.fn() };
    expect(() => parseMarkdownChunked(manager, "x".repeat(16_001), undefined, DESCRIPTION_BUDGET)).toThrow(DocumentBudgetExceeded);
    expect(manager.parse).not.toHaveBeenCalled();
  });

  it("allows a document exactly at the node budget", () => {
    const manager = { parse: () => ({ type: "doc", content: [{ type: "paragraph" }] }) };
    expect(parseMarkdownChunked(manager, "a\n\nb", 1, { maxNodes: 3, maxChunkCharacters: 100 }).content).toHaveLength(2);
  });
  it("keeps ordinary paragraph chunks small enough to avoid quadratic scans", () => {
    const markdown = Array.from(
      { length: 240 },
      (_, index) => `${index}: ${"x".repeat(92)}`,
    ).join("\n\n");
    const parsedLengths: number[] = [];
    const manager = {
      parse: vi.fn((chunk: string) => {
        parsedLengths.push(chunk.length);
        return {
          type: "doc",
          content: [{ type: "paragraph", attrs: { sourceLength: chunk.length } }],
        };
      }),
    };

    const result = parseMarkdownChunked(manager, markdown);

    expect(markdown.length).toBeGreaterThan(22_000);
    expect(manager.parse).toHaveBeenCalled();
    expect(parsedLengths.length).toBeGreaterThan(1);
    expect(Math.max(...parsedLengths)).toBeLessThan(6_000);
    expect(result.content).toHaveLength(parsedLengths.length);
  });
});
