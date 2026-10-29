// Electron GUI children must not inherit a parent's Node-only execution mode.
function envForElectron(env = process.env) {
  const childEnv = { ...env };
  for (const key of Object.keys(childEnv)) {
    // Windows environment names are case-insensitive.
    if (key.toUpperCase() === "ELECTRON_RUN_AS_NODE") delete childEnv[key];
  }
  return childEnv;
}

module.exports = { envForElectron };
