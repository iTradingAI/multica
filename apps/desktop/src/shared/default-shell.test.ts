// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  gitBashCandidatePaths,
  resolveDefaultShell,
  resolveGitBash,
} from "./default-shell";

describe("gitBashCandidatePaths", () => {
  it("covers the common install locations in priority order", () => {
    expect(
      gitBashCandidatePaths({
        ProgramFiles: "D:\\Program Files",
        "ProgramFiles(x86)": "D:\\Program Files (x86)",
        LOCALAPPDATA: "C:\\Users\\u\\AppData\\Local",
      }),
    ).toEqual([
      "D:\\Program Files\\Git\\bin\\bash.exe",
      "D:\\Program Files (x86)\\Git\\bin\\bash.exe",
      "C:\\Users\\u\\AppData\\Local\\Programs\\Git\\bin\\bash.exe",
    ]);
  });

  it("falls back to the default Program Files and skips LOCALAPPDATA when unset", () => {
    expect(gitBashCandidatePaths({})).toEqual([
      "C:\\Program Files\\Git\\bin\\bash.exe",
      "C:\\Program Files (x86)\\Git\\bin\\bash.exe",
    ]);
  });
});

describe("resolveGitBash", () => {
  it("returns the first candidate that exists", () => {
    const candidates = ["a\\bash.exe", "b\\bash.exe"];
    expect(resolveGitBash(candidates, (p) => p === "b\\bash.exe")).toBe(
      "b\\bash.exe",
    );
  });

  it("returns null when nothing exists", () => {
    expect(resolveGitBash(["a\\bash.exe"], () => false)).toBeNull();
    expect(resolveGitBash([], () => true)).toBeNull();
  });
});

describe("resolveDefaultShell", () => {
  it("prefers Git Bash over COMSPEC on Windows", () => {
    const candidates = gitBashCandidatePaths({});
    const bash = candidates[0];
    expect(
      resolveDefaultShell(
        "win32",
        { COMSPEC: "C:\\Windows\\system32\\cmd.exe" },
        (p) => p === bash,
      ),
    ).toEqual({ file: bash });
  });

  it("picks the first existing Git Bash location", () => {
    const candidates = gitBashCandidatePaths({});
    const second = candidates[1];
    if (!second) throw new Error("expected an x86 candidate");
    expect(
      resolveDefaultShell("win32", {}, (p) => p === second),
    ).toEqual({ file: second });
  });

  it("falls back to COMSPEC / PowerShell when Git Bash is absent", () => {
    expect(
      resolveDefaultShell(
        "win32",
        { COMSPEC: "C:\\Windows\\system32\\cmd.exe" },
        () => false,
      ),
    ).toEqual({ file: "C:\\Windows\\system32\\cmd.exe" });
    expect(resolveDefaultShell("win32", {}, () => false)).toEqual({
      file: "powershell.exe",
    });
  });

  it("keeps $SHELL first on unix-likes, Git Bash irrelevant", () => {
    expect(
      resolveDefaultShell("linux", { SHELL: "/usr/bin/fish" }, () => true),
    ).toEqual({ file: "/usr/bin/fish" });
    expect(resolveDefaultShell("darwin", {}, () => true)).toEqual({
      file: "/bin/zsh",
    });
    expect(resolveDefaultShell("linux", {}, () => true)).toEqual({
      file: "/bin/bash",
    });
  });
});
