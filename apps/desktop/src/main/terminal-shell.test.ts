// @vitest-environment node
import { describe, expect, it } from "vitest";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  fsIsDirectory,
  fsIsExecutableFile,
  resolveDefaultShell,
  shellEnv,
} from "./terminal-shell";

describe("resolveDefaultShell", () => {
  it("prefers $SHELL on unix-likes", () => {
    expect(resolveDefaultShell("linux", { SHELL: "/usr/bin/fish" })).toEqual({
      file: "/usr/bin/fish",
    });
    expect(
      resolveDefaultShell("darwin", { SHELL: "/opt/homebrew/bin/zsh" }),
    ).toEqual({ file: "/opt/homebrew/bin/zsh" });
  });

  it("falls back per platform when $SHELL is unset", () => {
    expect(resolveDefaultShell("darwin", {})).toEqual({ file: "/bin/zsh" });
    expect(resolveDefaultShell("linux", {})).toEqual({ file: "/bin/bash" });
  });

  it("uses COMSPEC on Windows when Git Bash is absent", () => {
    expect(
      resolveDefaultShell("win32", {
        COMSPEC: "C:\\Windows\\system32\\cmd.exe",
      }, () => false),
    ).toEqual({ file: "C:\\Windows\\system32\\cmd.exe" });
    expect(resolveDefaultShell("win32", {}, () => false)).toEqual({
      file: "powershell.exe",
    });
  });

  it("prefers Git Bash over COMSPEC on Windows when installed", () => {
    const bash = "C:\\Program Files\\Git\\bin\\bash.exe";
    expect(
      resolveDefaultShell("win32", { COMSPEC: "C:\\Windows\\cmd.exe" }, (p) => p === bash),
    ).toEqual({ file: bash });
  });
});

describe("shellEnv", () => {
  it("strips undefined values and pins TERM", () => {
    const env = shellEnv({
      PATH: "/usr/bin",
      HOME: "/home/user",
      EMPTY: undefined,
    });
    expect(env).toEqual({
      PATH: "/usr/bin",
      HOME: "/home/user",
      TERM: "xterm-256color",
    });
  });

  it("overrides an inherited TERM", () => {
    expect(shellEnv({ TERM: "dumb" }).TERM).toBe("xterm-256color");
  });
});

describe("filesystem validators", () => {
  it("accepts an existing directory and rejects files/missing paths", () => {
    const dir = mkdtempSync(join(tmpdir(), "terminal-shell-"));
    const file = join(dir, "script.sh");
    writeFileSync(file, "#!/bin/sh\n", { mode: 0o755 });
    try {
      expect(fsIsDirectory(dir)).toBe(true);
      expect(fsIsDirectory(file)).toBe(false);
      expect(fsIsDirectory(join(dir, "missing"))).toBe(false);
      expect(fsIsExecutableFile(file)).toBe(true);
      expect(fsIsExecutableFile(dir)).toBe(false);
      expect(fsIsExecutableFile(join(dir, "missing"))).toBe(false);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });
});
