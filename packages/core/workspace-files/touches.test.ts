import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "../api/client";

afterEach(() => vi.unstubAllGlobals());
const id = "123e4567-e89b-42d3-a456-426614174000";
const coverage = {
  unknown: 0,
  unavailable: 0,
  uncertain: 0,
  conflicting: 0,
  pending: 0,
};
function respond(body: unknown) {
  const fetch = vi
    .fn()
    .mockResolvedValue(
      new Response(JSON.stringify(body), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
  vi.stubGlobal("fetch", fetch);
  return fetch;
}
describe("FileTouchAPIResponseBoundary", () => {
  it("preserves a valid fixed identity and cancellation signal", async () => {
    const fetch = respond({
      resource_id: id,
      path: "src/safe.txt",
      binding_generation: 1,
    });
    const signal = new AbortController().signal;
    const value = await new ApiClient(
      "https://api.example.test",
    ).resolveIssueFileTouch(id, id, signal);
    expect(value.path).toBe("src/safe.txt");
    expect(fetch.mock.calls[0]?.[1]?.signal).toBe(signal);
  });
  for (const path of [
    "../private",
    "C:/private",
    "/private",
    "a\\private",
    "a\u0000.txt",
  ])
    it("rejects unsafe selection paths", async () => {
      respond({ resource_id: id, path, binding_generation: 1 });
      await expect(
        new ApiClient("https://api.example.test").resolveIssueFileTouch(id, id),
      ).rejects.toThrow("Malformed");
    });
  it("rejects missing generation and extra private evidence", async () => {
    for (const body of [
      { resource_id: id, path: "safe.txt" },
      {
        resource_id: id,
        path: "safe.txt",
        binding_generation: 1,
        cwd: "private",
      },
    ]) {
      respond(body);
      await expect(
        new ApiClient("https://api.example.test").resolveIssueFileTouch(id, id),
      ).rejects.toThrow("Malformed");
    }
  });
  it("keeps unavailable coverage and refuses malformed successful lists", async () => {
    respond({
      files: [],
      coverage: { ...coverage, unavailable: 2 },
      next_cursor: "",
    });
    expect(
      (await new ApiClient("https://api.example.test").listIssueFileTouches(id))
        .coverage.unavailable,
    ).toBe(2);
    respond({ files: "malformed", coverage, next_cursor: "" });
    await expect(
      new ApiClient("https://api.example.test").listIssueFileTouches(id),
    ).rejects.toThrow("Malformed");
  });
});
