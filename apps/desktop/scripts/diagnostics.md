# Isolated Electron diagnostics

GUI launchers must pass a copy of the environment through `envForElectron`
from `electron-env.cjs` before spawning Electron. This removes
`ELECTRON_RUN_AS_NODE`, including Windows case variants, without changing the
parent. Both `dev` and `preview` use `electron-vite.mjs` as that boundary.
Build-only tools and Node-side executable lookup (`require("electron")`
returning the executable path) do not need a GUI module guard.

`fixtures/electron.cjs` is a minimal guarded entry for new diagnostic harnesses.
It loads an optional URL argument, defaults to `about:blank`, and never loads
the application, daemon, credentials, or backend configuration.

From the repository root in Git Bash:

```bash
ELECTRON_RUN_AS_NODE=1 node apps/desktop/scripts/verify-electron-harness.mjs
```

Optional arguments are an entry path and a JSON evidence destination. The
destination's parent directory must exist. When supplied, a PNG is also saved
next to the JSON. The check first runs the entry with Node mode enabled and
requires an actionable error with exit code 1. It then launches a real GUI
with the variable removed and a fresh temporary profile, creates a 3s renderer
stall, and asserts the watchdog event contains `name` and `attribution` with
`containerName`/`containerSrc`. The windows and local HTTP server close before
the command returns. The temporary profile path is recorded for inspection.

The smoke check compiles the real watchdog and intercepts its `captureEvent`
dependency locally. It verifies the browser observation and event payload;
it does not verify analytics ingestion. It runs only when explicitly invoked,
outside the default desktop test suite.

The watchdog still uses a 2s threshold and 60s cooldown. Empty attribution is
valid for top-level tasks; these fields describe a frame/container and do not
identify a JavaScript function. Use a CPU profile when function attribution is
needed. See the [Long Tasks API attribution documentation](https://developer.mozilla.org/en-US/docs/Web/API/PerformanceLongTaskTiming/attribution).
