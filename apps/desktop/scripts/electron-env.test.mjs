// @vitest-environment node
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";
import { describe, expect, it, vi } from "vitest";
import ts from "typescript";
import electronEnv from "./electron-env.cjs";

describe("Electron launch environment", () => {
  it.each(["dev", "preview"])("filters the actual %s launch while forwarding CLI args", (command) => {
    const source = readFileSync(new URL("./electron-vite.mjs", import.meta.url), "utf8");
    const compiled = ts.transpileModule(source, {
      compilerOptions: { module: ts.ModuleKind.CommonJS, esModuleInterop: true },
    }).outputText;
    const spawnSync = vi.fn(() => ({ status: 0 }));
    const exit = vi.fn();
    const env = { ELECTRON_RUN_AS_NODE: "1", Path: "system-bins", DESKTOP_APP_SUFFIX: "isolated" };
    runInNewContext(compiled, {
      exports: {}, console,
      process: { env, argv: ["node", "electron-vite.mjs", command, "--mode", "staging"], platform: "win32", exit },
      require: (name) => {
        if (name === "node:child_process") return { spawnSync };
        if (name === "./electron-env.cjs") return electronEnv;
        if (name === "./package.mjs") return { envWithLocalBins: (input = env) => ({ ...input, Path: "local-bins;" + input.Path }) };
        throw new Error(`Unexpected dependency: ${name}`);
      },
    });
    expect(spawnSync).toHaveBeenCalledWith("electron-vite", [command, "--mode", "staging"], {
      stdio: "inherit", shell: true,
      env: { Path: "local-bins;system-bins", DESKTOP_APP_SUFFIX: "isolated" },
    });
    expect(env.ELECTRON_RUN_AS_NODE).toBe("1");
    expect(exit).toHaveBeenCalledWith(0);
  });

  it("strips Node mode, preserves other settings, and leaves the parent untouched", () => {
    const parent = {
      ELECTRON_RUN_AS_NODE: "1",
      Electron_Run_As_Node: "1",
      PATH: "local-bins",
      DESKTOP_APP_SUFFIX: "isolated-test",
    };
    expect(electronEnv.envForElectron(parent)).toEqual({
      PATH: "local-bins",
      DESKTOP_APP_SUFFIX: "isolated-test",
    });
    expect(parent.ELECTRON_RUN_AS_NODE).toBe("1");
  });

  it.each(["/electron.exe", undefined, null, {}, { app: undefined }])(
    "exits with an actionable message before accessing app for %j",
    (electron) => {
      const error = vi.fn();
      const exit = vi.fn(() => { throw new Error("exited"); });
      const source = readFileSync(new URL("./fixtures/electron.cjs", import.meta.url), "utf8");
      expect(() => runInNewContext(source, {
        require: () => electron,
        console: { error },
        process: { execPath: "test electron.exe", exit },
        __filename: "electron.cjs",
      })).toThrow("exited");
      expect(exit).toHaveBeenCalledWith(1);
      expect(error).toHaveBeenCalledWith(expect.stringContaining(
        'ELECTRON_RUN_AS_NODE is set, run via: env -u ELECTRON_RUN_AS_NODE "test electron.exe" "electron.cjs"',
      ));
    },
  );
});
