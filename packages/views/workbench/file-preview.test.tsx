import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { FileText } from "./file-preview";

describe("FilePreviewSafetyAndLimits", () => {
  it("never renders raw HTML, SVG, scripts, images or dangerous links", () => {
    const { container } = render(
      <FileText
        path="fixture.md"
        text={[
          "<script>alert(1)</script>",
          '<svg onload="alert(1)"></svg>',
          '<iframe src="https://example.test"></iframe>',
          "![remote](https://example.test/image)",
          "![local](image.png)",
          "[unsafe](javascript:alert%281%29)",
          "[relative](../file)",
          "[safe](https://example.test/page)",
        ].join("\n\n")}
      />,
    );
    expect(container.querySelector("script,svg,iframe,img")).toBeNull();
    expect(container.querySelectorAll("a")).toHaveLength(1);
    expect(screen.getByRole("link", { name: "safe" })).toHaveAttribute(
      "href",
      "https://example.test/page",
    );
  });
  it("opens safe links only after an explicit click", () => {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    render(
      <FileText path="fixture.md" text="[safe](https://example.test/page)" />,
    );
    expect(open).not.toHaveBeenCalled();
    screen.getByRole("link", { name: "safe" }).click();
    expect(open).toHaveBeenCalledWith(
      "https://example.test/page",
      "_blank",
      "noopener,noreferrer",
    );
    open.mockRestore();
  });
  it("renders source as text while highlighting registered languages", () => {
    const { container } = render(
      <FileText
        path="fixture.js"
        text={'const x = "<img src=x onerror=alert(1)>";'}
      />,
    );
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("code")?.textContent).toBe(
      'const x = "<img src=x onerror=alert(1)>";',
    );
    expect(container.querySelector("span.hljs-keyword")).not.toBeNull();
  });
});
