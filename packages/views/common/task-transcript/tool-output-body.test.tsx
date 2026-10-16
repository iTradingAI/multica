import { createHash } from "node:crypto";
import { cleanup, fireEvent, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../test/i18n";
import { StepBody } from "./agent-transcript-dialog";
import { buildTimeline } from "./build-timeline";
import { traceEventCopyText } from "./trace-event-presenter";
import { TOOL_OUTPUT_MAX_CHARS, TOOL_OUTPUT_MAX_LINES } from "./tool-output-preview";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

function record(output: string, output_truncated = false) {
  return Object.freeze({ task_id: "task", issue_id: "issue", seq: 2,
    type: "tool_result" as const, tool: "exec_command", output, output_truncated });
}

describe("shared tool result body", () => {
  it.each([117 * 1024, 456 * 1024, 1024 * 1024])("keeps %i characters bounded even after Show all", (size) => {
    const source = record("x".repeat(size));
    const [item] = buildTimeline([source]);
    const { container } = renderWithI18n(<StepBody item={item!} />);
    expect(screen.getByRole("button", { name: "Download original record" })).toBeVisible();
    expect(container.querySelector("pre")!.textContent!.length).toBeLessThanOrEqual(TOOL_OUTPUT_MAX_CHARS);
    fireEvent.click(screen.getByRole("button", { name: "Show all" }));
    expect(container.querySelector("pre")!.textContent!.length).toBeLessThanOrEqual(TOOL_OUTPUT_MAX_CHARS);
    expect(source.output).toHaveLength(size);
    expect(traceEventCopyText(item!)).toContain(source.output);
  });

  it("also bounds direct/live items and encoded blank lines before DOM mounting", () => {
    const source = record(JSON.stringify("\r\n".repeat(100_000)));
    const { container } = renderWithI18n(<StepBody item={source} />);
    expect(container.querySelector("pre")!.textContent!.split("\n").length).toBeLessThanOrEqual(TOOL_OUTPUT_MAX_LINES);
    expect(container.textContent!.length).toBeLessThan(2000);
  });

  it.each([false, true])("downloads unchanged UTF-8 record bytes (JSON wrapped: %s)", async (wrapped) => {
    vi.useFakeTimers();
    const text = `original\r\n中文 😀\n${"line\r\n".repeat(200_000)}TAIL\r\n`;
    const source = record(wrapped ? JSON.stringify(text) : text);
    const [item] = buildTimeline([source]);
    let blob: Blob | undefined;
    const revoke = vi.fn();
    vi.stubGlobal("URL", class extends URL {
      static createObjectURL(value: Blob) { blob = value; return "blob:fixture"; }
      static revokeObjectURL = revoke;
    });
    let filename: string | undefined;
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      filename = this.download;
    });
    renderWithI18n(<StepBody item={item!} />);
    fireEvent.click(screen.getByRole("button", { name: "Download original record" }));
    expect(click).toHaveBeenCalledOnce();
    expect(filename).toBe("tool-output-2.txt");
    vi.advanceTimersByTime(1000);
    expect(revoke).toHaveBeenCalledWith("blob:fixture");
    vi.useRealTimers();
    const bytes = await new Promise<ArrayBuffer>((resolve) => {
      const reader = new FileReader();
      reader.onload = () => resolve(reader.result as ArrayBuffer);
      reader.readAsArrayBuffer(blob!);
    });
    const actualBytes = Buffer.from(bytes);
    const expectedBytes = Buffer.from(source.output, "utf8");
    expect(actualBytes.byteLength).toBe(expectedBytes.byteLength);
    // Preserve exact byte equality without a matcher traversing millions of
    // indexed properties, which exceeded the test timeout on the CI shard.
    expect(actualBytes.equals(expectedBytes)).toBe(true);
    expect(createHash("sha256").update(actualBytes).digest("hex"))
      .toBe(createHash("sha256").update(source.output).digest("hex"));
    expect(item!.originalOutput).toBe(source.output);
  });

  it("keeps secret redaction on the preview and the source record unchanged", () => {
    const token = `sk-${"a".repeat(30)}`;
    const source = record(`${token}\r\n${"x".repeat(1024 * 1024)}`);
    const [item] = buildTimeline([source]);
    renderWithI18n(<StepBody item={item!} />);
    expect(screen.getByText(/REDACTED API KEY/)).toBeInTheDocument();
    expect(document.body.textContent).not.toContain(token);
    expect(item!.originalOutput).toBe(source.output);
  });

  it("keeps source truncation separate from a recoverable preview limit", () => {
    renderWithI18n(<StepBody item={buildTimeline([record("x".repeat(1024 * 1024), true)])[0]!} />);
    expect(screen.getByText(/only a preview is shown/)).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "Show all" }));
    expect(screen.getByText(/only the beginning was kept/)).toBeVisible();
  });

  it("preserves short text and small images without adding a download notice", () => {
    const { rerender } = renderWithI18n(<StepBody item={record(JSON.stringify("normal\r\noutput"))} />);
    expect(screen.getByText(/normal/).textContent).toBe("normal\r\noutput");
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    rerender(<StepBody item={record(JSON.stringify({ type: "image", source: { data: "base64", media_type: "image/png" } }))} />);
    expect(screen.getByRole("img", { name: "Image" })).toHaveAttribute("src", "data:image/png;base64,base64");
  });

  it("does not decode or mount a large base64 image", () => {
    const output = JSON.stringify({ type: "image", source: { type: "base64", data: "a".repeat(1024 * 1024) } });
    renderWithI18n(<StepBody item={record(output)} />);
    expect(screen.queryByRole("img")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Download original record" })).toBeVisible();
  });
});
