"use strict";

const fs = require("fs");
const path = require("path");
const { spawn } = require("child_process");

module.exports = function run(name) {
  const bin = path.join(__dirname, "..", "vendor", name);
  if (!fs.existsSync(bin)) {
    process.stderr.write(
      `clawdchan: binary not found at ${bin}\n` +
        `the postinstall step didn't run or failed. try:\n` +
        `  npm rebuild clawdchan\n` +
        `or install from source: https://github.com/agents-first/clawdchan\n`
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
