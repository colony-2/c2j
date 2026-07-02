#!/usr/bin/env node

"use strict";

const fs = require("fs");
const path = require("path");
const { spawnSync } = require("child_process");

const packageRoot = path.resolve(__dirname, "..");
const packageJson = require(path.join(packageRoot, "package.json"));
const packageName = packageJson.name.split("/").pop();
const binaryPath = path.join(packageRoot, "vendor", packageName);

if (!fs.existsSync(binaryPath)) {
  console.error(
    `${packageName} binary is missing. Reinstall the package or run npm rebuild ${packageJson.name}.`
  );
  process.exit(1);
}

const result = spawnSync(binaryPath, process.argv.slice(2), {
  stdio: "inherit",
});

if (result.error) {
  console.error(result.error.message);
  process.exit(1);
}

process.exit(result.status === null ? 1 : result.status);
