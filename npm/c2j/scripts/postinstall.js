#!/usr/bin/env node

"use strict";

const crypto = require("crypto");
const fs = require("fs");
const os = require("os");
const path = require("path");
const { execFileSync } = require("child_process");
const { get } = require("https");

const packageRoot = path.resolve(__dirname, "..");
const packageJson = require(path.join(packageRoot, "package.json"));
const packageName = packageJson.name.split("/").pop();
const version = packageJson.version;

const platforms = {
  "linux:x64": ["Linux", "x86_64"],
  "linux:arm64": ["Linux", "arm64"],
  "darwin:x64": ["Darwin", "x86_64"],
  "darwin:arm64": ["Darwin", "arm64"],
};

const platformKey = `${process.platform}:${process.arch}`;
const target = platforms[platformKey];

if (!target) {
  throw new Error(`Unsupported platform for ${packageJson.name}: ${platformKey}`);
}

const repo = repositoryPath(packageJson);
const tag = `v${version}`;
const assetName = `${packageName}_${version}_${target[0]}_${target[1]}.tar.gz`;
const baseUrl = `https://github.com/${repo}/releases/download/${tag}`;
const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), `${packageName}-`));
const archivePath = path.join(tempDir, assetName);
const checksumsPath = path.join(tempDir, "checksums.txt");
const extractDir = path.join(tempDir, "extract");
const vendorDir = path.join(packageRoot, "vendor");
const installedBinary = path.join(vendorDir, packageName);

fs.mkdirSync(extractDir);
fs.mkdirSync(vendorDir, { recursive: true });

downloadWithRetry(`${baseUrl}/${assetName}`, archivePath)
  .then(() => downloadWithRetry(`${baseUrl}/checksums.txt`, checksumsPath))
  .then(() => {
    verifyChecksum(archivePath, checksumsPath, assetName);
    execFileSync("tar", ["-xzf", archivePath, "-C", extractDir], {
      stdio: "inherit",
    });

    const extractedBinary = path.join(extractDir, packageName);
    if (!fs.existsSync(extractedBinary)) {
      throw new Error(`Release archive did not contain ${packageName}`);
    }

    fs.copyFileSync(extractedBinary, installedBinary);
    fs.chmodSync(installedBinary, 0o755);
  })
  .catch((error) => {
    console.error(error.message || error);
    process.exitCode = 1;
  })
  .finally(() => {
    fs.rmSync(tempDir, { recursive: true, force: true });
  });

function repositoryPath(pkg) {
  const raw =
    typeof pkg.repository === "string" ? pkg.repository : pkg.repository && pkg.repository.url;

  if (!raw) {
    throw new Error("package.json repository is required to locate release assets");
  }

  const match = raw.match(/github\.com[:/]([^/]+\/[^/.]+)(?:\.git)?/);
  if (!match) {
    throw new Error(`Unsupported repository URL for release assets: ${raw}`);
  }

  return match[1];
}

async function downloadWithRetry(url, destination) {
  let lastError;

  for (let attempt = 1; attempt <= 5; attempt += 1) {
    try {
      await download(url, destination);
      return;
    } catch (error) {
      lastError = error;
      if (attempt === 5) {
        break;
      }
      await delay(attempt * 1000);
    }
  }

  throw lastError;
}

function download(url, destination) {
  return new Promise((resolve, reject) => {
    const request = get(url, (response) => {
      if (
        response.statusCode >= 300 &&
        response.statusCode < 400 &&
        response.headers.location
      ) {
        response.resume();
        download(response.headers.location, destination).then(resolve, reject);
        return;
      }

      if (response.statusCode !== 200) {
        response.resume();
        reject(new Error(`Failed to download ${url}: HTTP ${response.statusCode}`));
        return;
      }

      const file = fs.createWriteStream(destination, { mode: 0o600 });
      response.pipe(file);
      file.on("finish", () => file.close(resolve));
      file.on("error", reject);
    });

    request.on("error", reject);
  });
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function verifyChecksum(archive, checksums, assetName) {
  const expected = fs
    .readFileSync(checksums, "utf8")
    .split(/\r?\n/)
    .map((line) => line.trim().split(/\s+/))
    .find((parts) => parts[1] === assetName);

  if (!expected) {
    throw new Error(`No checksum found for ${assetName}`);
  }

  const actual = crypto.createHash("sha256").update(fs.readFileSync(archive)).digest("hex");
  if (actual !== expected[0]) {
    throw new Error(`Checksum mismatch for ${assetName}`);
  }
}
