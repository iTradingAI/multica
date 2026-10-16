import { createRef } from "react";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import { BoundedContentEditor } from "./bounded-content-editor";
import type { ContentEditorRef } from "./content-editor";
import en from "../locales/en/editor.json";

vi.mock("../i18n", () => ({
  useT: () => ({ t: (select: (resources: typeof en) => string) => select(en) }),
}));

function host(value: string, onUpdate: (markdown: string, baseMarkdown: string) => void, ref = createRef<ContentEditorRef>()) {
  return <QueryClientProvider client={new QueryClient()}>
    <BoundedContentEditor value={value} onUpdate={onUpdate} ref={ref} showBubbleMenu={false} flushPendingOnUnmount />
  </QueryClientProvider>;
}

describe("bounded issue description", () => {
  it("never mounts Tiptap or emits collapsed bytes, and downloads the exact source", async () => {
    const value = `# Original\r\n${"\r\n".repeat(425_984)}End`;
    const onUpdate = vi.fn();
    const ref = createRef<ContentEditorRef>();
    const createUrl = vi.fn((_blob: Blob) => "blob:original");
    const revokeUrl = vi.fn();
    vi.stubGlobal("URL", Object.assign(URL, { createObjectURL: createUrl, revokeObjectURL: revokeUrl }));
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    const view = render(host(value, onUpdate, ref));
    expect(view.container.querySelector(".ProseMirror")).toBeNull();
    expect(ref.current).toBeNull();
    expect(view.container.querySelector("pre")?.textContent).toBe("# Original\n\nEnd");
    fireEvent.click(screen.getByRole("button", { name: en.large_document.download }));
    const blob = createUrl.mock.calls[0]![0] as Blob;
    const downloaded = await new Promise((resolve) => {
      const reader = new FileReader();
      reader.onload = () => resolve(reader.result);
      reader.readAsText(blob);
    });
    expect(downloaded).toBe(value);
    view.unmount();
    expect(onUpdate).not.toHaveBeenCalled();
    click.mockRestore();
    vi.unstubAllGlobals();
  });

  it("preserves ordinary editing and switches clean realtime updates to preview and back without writes", async () => {
    const onUpdate = vi.fn();
    const ref = createRef<ContentEditorRef>();
    const view = render(host("# Normal\n\nBody", onUpdate, ref));
    await waitFor(() => expect(ref.current?.getMarkdown()).toContain("Body"));
    expect(view.container.querySelector(".ProseMirror")).not.toBeNull();
    view.rerender(host("start\n" + "\n".repeat(5_000) + "end", onUpdate, ref));
    await waitFor(() => expect(view.container.querySelector(".ProseMirror")).toBeNull());
    expect(view.container.querySelector("pre")?.textContent).toContain("end");
    view.rerender(host("Restored", onUpdate, ref));
    await waitFor(() => expect(ref.current?.getMarkdown()).toBe("Restored"));
    view.unmount();
    expect(onUpdate).not.toHaveBeenCalled();
  });

  it("rejects a dense inline document before ProseMirror creates thousands of nodes", async () => {
    const onUpdate = vi.fn();
    const view = render(host("**a**b".repeat(2_600), onUpdate));
    await waitFor(() => expect(view.container.querySelector("[data-large-description-preview]")).not.toBeNull());
    expect(view.container.querySelector(".ProseMirror")).toBeNull();
    expect(view.container.querySelectorAll("*").length).toBeLessThan(15);
    view.unmount();
    expect(onUpdate).not.toHaveBeenCalled();
  });

  it("applies the same source guard to imperative external adoption", async () => {
    const onUpdate = vi.fn();
    const ref = createRef<ContentEditorRef>();
    const view = render(host("Original", onUpdate, ref));
    await waitFor(() => expect(ref.current?.getMarkdown()).toBe("Original"));
    act(() => ref.current?.adoptContent("large\n" + "\n".repeat(5_000) + "end"));
    await waitFor(() => expect(view.container.querySelector(".ProseMirror")).toBeNull());
    expect(view.container.querySelector("pre")?.textContent).toContain("large");
    view.unmount();
    expect(onUpdate).not.toHaveBeenCalled();
  });

  it("keeps an unsaved local edit when a large realtime value arrives", async () => {
    const onUpdate = vi.fn();
    const ref = createRef<ContentEditorRef>();
    const view = render(host("Original", onUpdate, ref));
    await waitFor(() => expect(ref.current?.getMarkdown()).toBe("Original"));
    act(() => { ref.current?.insertMarkdownAtEnd(" typed"); });
    view.rerender(host("incoming\n" + "\n".repeat(5_000), onUpdate, ref));
    expect(view.container.querySelector(".ProseMirror")).not.toBeNull();
    expect(ref.current?.getMarkdown()).toContain("typed");
    await waitFor(() => expect(onUpdate).toHaveBeenCalledTimes(1));
    expect(onUpdate.mock.calls[0]?.[0]).toContain("typed");
    expect(onUpdate.mock.calls[0]?.[1]).toBe("Original");
    view.unmount();
    expect(onUpdate).toHaveBeenCalledTimes(1);
  });
});
