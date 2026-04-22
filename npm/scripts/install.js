#!/usr/bin/env node
/*
 * Post-install: fetch the matching prebuilt clawdchan archive from GitHub
 * Releases and extract the three binaries into ./vendor/.
 *
 *   CLAWDCHAN_VERSION=v0.1.0  pin to a specific tag (default: package.json .version,
 *                             or "latest" if the version isn't yet released)
 *   CLAWDCHAN_SKIP_POSTINSTALL=1  don't download (e.g. in CI that vendors elsewhere)
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
  // Exit 0 so `npm i` doesn't hard-fail — the bin shims print a friendly error if the binary is missing.
  process.exit(0);
});

async function main() {
  const tag = await resolveTag(VERSION);
  const version = tag.replace(/^v/, "");
  const archive = `clawdchan_${version}_${osName}_${archName}.tar.gz`;
  const base = `https://github.com/${REPO}/releases/download/${tag}`;

  log(`downloading clawdchan ${tag} (${osName}_${archName})`);

  fs.mkdirSync(VENDOR, { recursive: true });
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

    execFileSync("tar", ["-xzf", archivePath, "-C", VENDOR], { stdio: "ignore" });
    for (const b of ["clawdchan", "clawdchan-mcp", "clawdchan-relay"]) {
      const p = path.join(VENDOR, b);
      if (!fs.existsSync(p)) throw new Error(`missing ${b} in archive`);
      fs.chmodSync(p, 0o755);
    }
    log(`installed binaries to ${VENDOR}`);
    log("next: run `clawdchan setup`");
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
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
    const req = https.get(
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
    );
    req.on("error", reject);
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
