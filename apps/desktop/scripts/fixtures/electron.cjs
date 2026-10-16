// Minimal isolated GUI entry for diagnostic harnesses (no application/backend).
const electron = require("electron");
if (!electron || typeof electron !== "object" || !electron.app) {
  console.error(
    `ELECTRON_RUN_AS_NODE is set, run via: env -u ELECTRON_RUN_AS_NODE "${process.execPath}" "${__filename}"`,
  );
  process.exit(1);
}

const { app, BrowserWindow } = electron;
app.whenReady().then(() => {
  const window = new BrowserWindow({
    width: 800,
    height: 600,
    webPreferences: { contextIsolation: true, nodeIntegration: false },
  });
  window.loadURL(process.argv[2] || "about:blank");
});
app.on("window-all-closed", () => app.quit());
