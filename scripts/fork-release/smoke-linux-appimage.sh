set -euo pipefail

image=$(realpath "$1")
scratch=$(mktemp -d)
pid=
cleanup() {
  if [[ -n "$pid" ]]; then
    kill -TERM -- "-$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  fi
  if [[ -f "$scratch/desktop.log" ]]; then
    cat "$scratch/desktop.log"
  fi
  rm -r -- "$scratch"
}
trap cleanup EXIT
cd "$scratch"
export XDG_CONFIG_HOME="$scratch/config" XDG_DATA_HOME="$scratch/data" XDG_CACHE_HOME="$scratch/cache"
export ELECTRON_ENABLE_LOGGING=1
unset ELECTRON_RUN_AS_NODE
# The hosted VM may not expose FUSE. This still runs the AppImage entry point.
if [[ ! -r /dev/fuse || ! -w /dev/fuse ]]; then
  export APPIMAGE_EXTRACT_AND_RUN=1
fi
setsid "$image" > desktop.log 2>&1 &
pid=$!
timeout 60s xdotool search --sync --onlyvisible --class '^Multica$'
kill -0 "$pid"
if grep -Eq 'GLIBC(X{2})?_[^ ]+.*not found|CXXABI_[^ ]+.*not found|UnhandledPromiseRejection|App threw an error' desktop.log; then
  exit 1
fi
echo 'Packaged desktop window visible'
"$image" --appimage-extract > /dev/null
root="$scratch/squashfs-root"
timeout 30s env ELECTRON_RUN_AS_NODE=1 "$root/multica-desktop" - "$root" <<'JS'
const assert = require('node:assert/strict');
const { join } = require('node:path');
const root = process.argv[2];
const res = join(root, 'resources');
const voice = require(join(res, 'app.asar.unpacked/resources/voice-engine/sherpa-onnx.node'));
assert.equal(typeof voice.createOfflineRecognizer, 'function');
console.log('Packaged speech addon loaded');
const { spawn } = require(join(res, 'app.asar/node_modules/node-pty'));
const pty = spawn('/bin/sh', ['-c', 'printf "MULTICA_PTY_OK\n"'], { cols: 80, rows: 24 });
let output = '';
pty.onData((data) => { output += data; });
pty.onExit(({ exitCode }) => {
  assert.equal(exitCode, 0);
  assert.match(output, /MULTICA_PTY_OK\r?\n/);
  console.log('Packaged PTY spawned a shell and returned output');
});
JS
