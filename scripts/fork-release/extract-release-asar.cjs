// Use the same lockfile-pinned ASAR implementation as the desktop packager.
const { createRequire } = require("node:module");
const { resolve } = require("node:path");

const desktop = createRequire(resolve(__dirname, "../../apps/desktop/package.json"));
const builder = createRequire(desktop.resolve("electron-builder"));
const library = createRequire(builder.resolve("app-builder-lib"));
const { extractAll } = library("@electron/asar");

const [archive, destination] = process.argv.slice(2);
extractAll(archive, destination);
