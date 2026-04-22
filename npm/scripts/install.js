#!/usr/bin/env node
/*
 * Post-install: fetch the matching prebuilt clawdchan archive from GitHub
 * Releases and extract binaries into a stable location that survives
 * `npm uninstall` / `npx` cache GC.
 *
 * Install order:
 *   1. $CLAWDCHAN_INSTALL_DIR if set
 *   2. ~/.clawdchan/bin (preferred — matches the shell installer;
 *      keeps the launchd/systemd plist path stable across npm upgrades)
 *   3. <package>/vendor/ (fallback when home isn't writable, e.g. sudo npm i -g)
 *
 * The bin shims in ../bin/ prefer the stable path and fall back to vendor/.
 *
 *   CLAWDCHAN_VERSION=v0.1.0          pin a specific release tag
 *   CLAWDCHAN_SKIP_POSTINSTALL=1      skip the download entirely
 *   CLAWDCHAN_INSTALL_DIR=~/bin       override the install dir
 */

"use strict";

const fs = require("fs");
const os = require("os");
const path = require("path");
const https = require("https");
const crypto = require("crypto");
const { execFileSync } = require("child_process");

if (process.env.CLAWDCHAN_SKIP_POSTINSTALL) {
  log("skipped (CLAWDCHAN_SKIP_POSTINSTALL set)");
  process.exit(0);
}

const REPO = "agents-first/clawdchan";
const PKG = require("../package.json");
const VERSION = process.env.CLAWDCHAN_VERSION || `v${PKG.version}`;
const VENDOR = path.join(__dirname, "..", "vendor");
const BINS = ["clawdchan", "clawdchan-mcp", "clawdchan-relay"];

const OS_MAP = { darwin: "macOS", linux: "Linux" };
const ARCH_MAP = { x64: "x86_64", arm64: "arm64" };

const osName = OS_MAP[process.platform];
const archName = ARCH_MAP[process.arch];

if (!osName || !archName) {
  log(`unsupported platform ${process.platform}/${process.arch} — install from source: https://github.com/${REPO}`);
  process.exit(0);
}

main().catch((err) => {
  log(`install failed: ${err.message}`);
  log(`fetch manually from https://github.com/${REPO}/releases and place binaries in ${VENDOR}`);
  process.exit(0); // don't hard-fail npm i; shim prints a friendly error if the binary is missing
});

async function main() {
  const tag = await resolveTag(VERSION);
  const version = tag.replace(/^v/, "");
  const archive = `clawdchan_${version}_${osName}_${archName}.tar.gz`;
  const base = `https://github.com/${REPO}/releases/download/${tag}`;

  log(`downloading clawdchan ${tag} (${osName}_${archName})`);

  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "clawdchan-"));
  const archivePath = path.join(tmp, archive);

  try {
    await download(`${base}/${archive}`, archivePath);

    try {
      const sumsPath = path.join(tmp, "checksums.txt");
      await download(`${base}/checksums.txt`, sumsPath);
      verifyChecksum(archivePath, archive, sumsPath);
    } catch (e) {
      log(`checksum check skipped: ${e.message}`);
    }

    const installDir = chooseInstallDir();
    fs.mkdirSync(installDir, { recursive: true });
    execFileSync("tar", ["-xzf", archivePath, "-C", tmp], { stdio: "ignore" });

    for (const b of BINS) {
      const src = path.join(tmp, b);
      if (!fs.existsSync(src)) throw new Error(`missing ${b} in archive`);
      const dst = path.join(installDir, b);
      fs.renameSync(src, dst);
      fs.chmodSync(dst, 0o755);
    }
    log(`installed binaries to ${installDir}`);

    maybeNoteTerminalNotifier();

    log("next: run `clawdchan setup`");
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
}

function chooseInstallDir() {
  const override = process.env.CLAWDCHAN_INSTALL_DIR;
  if (override) return expandHome(override);

  const stable = path.join(os.homedir(), ".clawdchan", "bin");
  try {
    fs.mkdirSync(stable, { recursive: true });
    const probe = path.join(stable, `.wtest-${process.pid}`);
    fs.writeFileSync(probe, "");
    fs.unlinkSync(probe);
    return stable;
  } catch (e) {
    log(`cannot write to ${stable} (${e.code || e.message}); falling back to ${VENDOR}`);
    return VENDOR;
  }
}

function expandHome(p) {
  if (p === "~" || p.startsWith("~/")) return path.join(os.homedir(), p.slice(1));
  return p;
}

function maybeNoteTerminalNotifier() {
  if (process.platform !== "darwin") return;
  if (which("terminal-notifier")) return;
  if (which("brew")) {
    log("note: `terminal-notifier` is recommended on macOS (osascript banners are often dropped).");
    log("      install it with: brew install terminal-notifier");
  } else {
    log("note: install `terminal-notifier` via Homebrew for reliable macOS banner notifications.");
  }
  // Postinstall runs non-interactively under `npm i -g`; we don't prompt here.
  // `clawdchan daemon install` (invoked later by `clawdchan setup`) surfaces the same hint.
}

function which(cmd) {
  try {
    execFileSync("sh", ["-c", `command -v ${cmd}`], { stdio: ["ignore", "ignore", "ignore"] });
    return true;
  } catch {
    return false;
  }
}

function verifyChecksum(archivePath, archiveName, sumsPath) {
  const sums = fs.readFileSync(sumsPath, "utf8").split(/\r?\n/);
  const line = sums.find((l) => l.endsWith(` ${archiveName}`) || l.endsWith(`*${archiveName}`));
  if (!line) throw new Error(`archive not listed in checksums.txt`);
  const want = line.split(/\s+/)[0];
  const got = crypto.createHash("sha256").update(fs.readFileSync(archivePath)).digest("hex");
  if (want !== got) throw new Error(`checksum mismatch: want ${want}, got ${got}`);
}

function resolveTag(requested) {
  if (requested && requested !== "latest") return Promise.resolve(requested);
  return new Promise((resolve, reject) => {
    https
      .get(
        `https://api.github.com/repos/${REPO}/releases/latest`,
        { headers: { "User-Agent": "clawdchan-npm-installer" } },
        (res) => {
          if (res.statusCode !== 200) {
            reject(new Error(`GitHub API HTTP ${res.statusCode}`));
            return;
          }
          let body = "";
          res.on("data", (c) => (body += c));
          res.on("end", () => {
            try {
              const tag = JSON.parse(body).tag_name;
              if (!tag) throw new Error("no tag_name in response");
              resolve(tag);
            } catch (e) {
              reject(e);
            }
          });
        }
      )
      .on("error", reject);
  });
}

function download(url, dest) {
  return new Promise((resolve, reject) => {
    const file = fs.createWriteStream(dest);
    https
      .get(url, { headers: { "User-Agent": "clawdchan-npm-installer" } }, (res) => {
        if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
          file.close();
          fs.unlinkSync(dest);
          download(res.headers.location, dest).then(resolve, reject);
          return;
        }
        if (res.statusCode !== 200) {
          file.close();
          fs.unlinkSync(dest);
          reject(new Error(`HTTP ${res.statusCode} for ${url}`));
          return;
        }
        res.pipe(file);
        file.on("finish", () => file.close(resolve));
      })
      .on("error", (err) => {
        file.close();
        try { fs.unlinkSync(dest); } catch {}
        reject(err);
      });
  });
}

function log(msg) {
  process.stdout.write(`clawdchan: ${msg}\n`);
}
