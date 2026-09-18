// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  isTerminalSessionId,
  parseTerminalKillInput,
  parseTerminalResizeInput,
  parseTerminalSpawnRequest,
  parseTerminalWriteInput,
  TERMINAL_MAX_WRITE_BYTES,
} from "./terminal-types";

describe("isTerminalSessionId", () => {
  it("accepts a v4-shaped uuid", () => {
    expect(isTerminalSessionId("123e4567-e89b-42d3-a456-426614174000")).toBe(
      true,
    );
  });

  it("rejects non-uuid strings", () => {
    expect(isTerminalSessionId("abc")).toBe(false);
    expect(isTerminalSessionId("")).toBe(false);
    expect(isTerminalSessionId("session-1")).toBe(false);
    // Uppercase hex is not produced by crypto.randomUUID; reject it so the
    // accepted surface matches exactly what main mints.
    expect(isTerminalSessionId("123E4567-E89B-42D3-A456-426614174000")).toBe(
      false,
    );
  });

  it("rejects non-strings", () => {
    expect(isTerminalSessionId(123)).toBe(false);
    expect(isTerminalSessionId(null)).toBe(false);
    expect(isTerminalSessionId(undefined)).toBe(false);
  });
});

describe("parseTerminalSpawnRequest", () => {
  const valid = { cols: 80, rows: 24 };

  it("parses a minimal valid request", () => {
    expect(parseTerminalSpawnRequest(valid)).toEqual({ cols: 80, rows: 24 });
  });

  it("parses optional cwd and shell when provided", () => {
    expect(
      parseTerminalSpawnRequest({
        ...valid,
        cwd: "/home/user/project",
        shell: "/bin/zsh",
      }),
    ).toEqual({
      cols: 80,
      rows: 24,
      cwd: "/home/user/project",
      shell: "/bin/zsh",
    });
  });

  it("treats empty strings and null as absent", () => {
    expect(parseTerminalSpawnRequest({ ...valid, cwd: "" })).toEqual({
      cols: 80,
      rows: 24,
    });
    expect(parseTerminalSpawnRequest({ ...valid, cwd: null })).toEqual({
      cols: 80,
      rows: 24,
    });
  });

  it("rejects non-object payloads", () => {
    expect(parseTerminalSpawnRequest(null)).toBeNull();
    expect(parseTerminalSpawnRequest("spawn")).toBeNull();
    expect(parseTerminalSpawnRequest([valid])).toBeNull();
  });

  it("rejects missing or non-integer dimensions", () => {
    expect(parseTerminalSpawnRequest({ rows: 24 })).toBeNull();
    expect(parseTerminalSpawnRequest({ cols: 80 })).toBeNull();
    expect(
      parseTerminalSpawnRequest({ cols: 80.5, rows: 24 }),
    ).toBeNull();
    expect(parseTerminalSpawnRequest({ cols: "80", rows: 24 })).toBeNull();
  });

  it("rejects out-of-bounds dimensions", () => {
    expect(parseTerminalSpawnRequest({ cols: 1, rows: 24 })).toBeNull();
    expect(parseTerminalSpawnRequest({ cols: 501, rows: 24 })).toBeNull();
    expect(parseTerminalSpawnRequest({ cols: 80, rows: 1 })).toBeNull();
    expect(parseTerminalSpawnRequest({ cols: 80, rows: 301 })).toBeNull();
  });

  it("rejects relative and non-string cwd/shell", () => {
    expect(
      parseTerminalSpawnRequest({ ...valid, cwd: "relative/path" }),
    ).toBeNull();
    expect(
      parseTerminalSpawnRequest({ ...valid, shell: "zsh" }),
    ).toBeNull();
    expect(parseTerminalSpawnRequest({ ...valid, cwd: 42 })).toBeNull();
  });
});

describe("parseTerminalWriteInput", () => {
  const sessionId = "123e4567-e89b-42d3-a456-426614174000";

  it("parses a valid write", () => {
    expect(parseTerminalWriteInput({ sessionId, data: "ls\r" })).toEqual({
      sessionId,
      data: "ls\r",
    });
  });

  it("rejects a bad session id", () => {
    expect(
      parseTerminalWriteInput({ sessionId: "nope", data: "ls\r" }),
    ).toBeNull();
  });

  it("rejects non-string data", () => {
    expect(parseTerminalWriteInput({ sessionId, data: 42 })).toBeNull();
  });

  it("rejects a write above the byte cap", () => {
    const huge = "x".repeat(TERMINAL_MAX_WRITE_BYTES + 1);
    expect(parseTerminalWriteInput({ sessionId, data: huge })).toBeNull();
  });

  it("accepts a write at exactly the byte cap", () => {
    const edge = "x".repeat(TERMINAL_MAX_WRITE_BYTES);
    expect(parseTerminalWriteInput({ sessionId, data: edge })).toEqual({
      sessionId,
      data: edge,
    });
  });
});

describe("parseTerminalResizeInput", () => {
  const sessionId = "123e4567-e89b-42d3-a456-426614174000";

  it("parses a valid resize", () => {
    expect(
      parseTerminalResizeInput({ sessionId, cols: 120, rows: 40 }),
    ).toEqual({ sessionId, cols: 120, rows: 40 });
  });

  it("rejects bad ids and dimensions", () => {
    expect(parseTerminalResizeInput({ sessionId: "x", cols: 80, rows: 24 })).toBeNull();
    expect(parseTerminalResizeInput({ sessionId, cols: 0, rows: 24 })).toBeNull();
    expect(parseTerminalResizeInput({ sessionId, cols: 80, rows: 9999 })).toBeNull();
  });
});

describe("parseTerminalKillInput", () => {
  it("parses a valid kill", () => {
    const sessionId = "123e4567-e89b-42d3-a456-426614174000";
    expect(parseTerminalKillInput({ sessionId })).toEqual({ sessionId });
  });

  it("rejects anything but a valid session id object", () => {
    expect(parseTerminalKillInput({ sessionId: "x" })).toBeNull();
    expect(parseTerminalKillInput("123e4567-e89b-42d3-a456-426614174000")).toBeNull();
    expect(parseTerminalKillInput(null)).toBeNull();
  });
});
