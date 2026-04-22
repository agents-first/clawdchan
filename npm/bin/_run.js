"use strict";

const fs = require("fs");
const os = require("os");
const path = require("path");
const { spawn } = require("child_process");

const STABLE = path.join(os.homedir(), ".clawdchan", "bin");
const VENDOR = path.join(__dirname, "..", "vendor");

module.exports = function run(name) {
  const override = process.env.CLAWDCHAN_INSTALL_DIR;
  const candidates = [
    override && path.join(expandHome(override), name),
    path.join(STABLE, name),
    path.join(VENDOR, name),
  ].filter(Boolean);

  const bin = candidates.find((p) => fs.existsSync(p));
  if (!bin) {
    process.stderr.write(
      `clawdchan: binary '${name}' not found.\n` +
        `  searched: ${candidates.join(", ")}\n` +
        `  the postinstall step didn't run or failed. try:\n` +
        `    npm rebuild clawdchan\n` +
        `  or install from source: https://github.com/agents-first/clawdchan\n`
    );
    process.exit(1);
  }

  const child = spawn(bin, process.argv.slice(2), { stdio: "inherit" });
  child.on("exit", (code, signal) => {
    if (signal) process.kill(process.pid, signal);
    else process.exit(code ?? 0);
  });
  child.on("error", (err) => {
    process.stderr.write(`clawdchan: failed to spawn ${bin}: ${err.message}\n`);
    process.exit(1);
  });
};

function expandHome(p) {
  if (p === "~" || p.startsWith("~/")) return path.join(os.homedir(), p.slice(1));
  return p;
}
