// @vitest-environment node
import { describe, expect, it } from "vitest";
import { DESCRIPTION_BUDGET as budget, descriptionPreview, exceedsDescriptionBudget } from "./document-budget";

describe("description budget", () => {
  it("accepts ordinary descriptions and rejects each excessive source dimension", () => {
    expect(exceedsDescriptionBudget("# Heading\n\nA normal description.")).toBe(false);
    expect(exceedsDescriptionBudget("x".repeat(budget.maxCharacters))).toBe(false);
    expect(exceedsDescriptionBudget("x".repeat(budget.maxCharacters + 1))).toBe(true);
    expect(exceedsDescriptionBudget(Array(budget.maxLines).fill("x").join("\n"))).toBe(false);
    expect(exceedsDescriptionBudget(Array(budget.maxLines + 1).fill("x").join("\n"))).toBe(true);
    for (const newline of ["\n", "\r\n", "\r", "\r\r\n"]) {
      expect(exceedsDescriptionBudget(`start${newline.repeat(budget.maxBlankLines + 2)}end`)).toBe(true);
    }
    expect(exceedsDescriptionBudget(`start\n${" \t\n".repeat(budget.maxBlankLines)}end`)).toBe(false);
  });

  it("bounds plain text preview while collapsing blank lines without modifying the source", () => {
    const source = `# Start\r\n${"\r\n".repeat(425_984)}<script>not executed</script>\nEnd`;
    const preview = descriptionPreview(source);
    expect(preview).toBe("# Start\n\n<script>not executed</script>\nEnd");
    expect(source).toContain("\r\n".repeat(425_984));
    expect(descriptionPreview("x".repeat(100_000)).length).toBeLessThanOrEqual(12_000);
    expect(descriptionPreview("text\n".repeat(100_000)).split("\n").length).toBeLessThanOrEqual(120);
  });
});
