// @vitest-environment node
import { describe, expect, it } from "vitest";
import { boundToolOutput, TOOL_OUTPUT_MAX_CHARS, TOOL_OUTPUT_MAX_LINES } from "./tool-output-preview";

describe("tool output preview budget", () => {
  it.each([117 * 1024, 456 * 1024, 1024 * 1024])("bounds a %i-character output", (size) => {
    const raw = "x".repeat(size);
    const preview = boundToolOutput(raw);
    expect(preview.clipped).toBe(true);
    expect(preview.text.length).toBeLessThanOrEqual(TOOL_OUTPUT_MAX_CHARS);
    expect(raw).toHaveLength(size);
  });

  it.each(["\n", "\r", "\r\n"])("bounds newline-heavy output independently of the character limit (%j)", (newline) => {
    const preview = boundToolOutput(newline.repeat(100_000));
    expect(preview.clipped).toBe(true);
    expect(preview.text.split(/\r\n|\r|\n/).length).toBeLessThanOrEqual(TOOL_OUTPUT_MAX_LINES);
  });

  it.each(["plain\r\noutput", JSON.stringify("quoted\r\noutput"), '{"data":[1,2]}', "x".repeat(8192)])(
    "preserves the usual decoded display for %s", (raw) => {
      const expected = raw.startsWith('"') ? JSON.parse(raw) : raw;
      expect(boundToolOutput(raw)).toEqual({ text: expected, clipped: false });
    },
  );

  it("decodes a bounded prefix of a large JSON string without transport escaping", () => {
    const preview = boundToolOutput(JSON.stringify("line\r\n".repeat(100_000)));
    expect(preview.clipped).toBe(true);
    expect(preview.text.startsWith("line\r\nline\r\n")).toBe(true);
    expect(preview.text.split("\n").length).toBeLessThanOrEqual(TOOL_OUTPUT_MAX_LINES);
  });

  it("does not leave half an emoji or CRLF at the cut", () => {
    for (const suffix of ["😀tail", "\r\ntail"]) {
      const preview = boundToolOutput("x".repeat(TOOL_OUTPUT_MAX_CHARS - 1) + suffix);
      expect(preview.text).toBe("x".repeat(TOOL_OUTPUT_MAX_CHARS - 1));
    }
  });

  it("decodes a prefix ending inside each position of a JSON unicode escape", () => {
    for (let offset = 0; offset < 6; offset++) {
      const prefix = "x".repeat(TOOL_OUTPUT_MAX_CHARS - offset - 2);
      const preview = boundToolOutput(`"${prefix}\\u4e2d${"tail".repeat(20)}"`);
      expect(preview.clipped).toBe(true);
      expect(preview.text.startsWith(prefix)).toBe(true);
      expect(preview.text).not.toContain("\\u");
      expect(preview.text).not.toContain('"');
    }
  });
});
