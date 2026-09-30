#!/usr/bin/env node
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, join } from "node:path";

const packageMetadata = JSON.parse(readFileSync(new URL("./package.json", import.meta.url), "utf-8"));
const PACKAGE_NAME = `npm:${packageMetadata.name}@${packageMetadata.version}`;
const LEGACY_PACKAGE_NAMES = new Set([
  "npm:gentle-engram@0.1.8",
  "npm:gentle-engram@0.1.11",
  "npm:gentle-engram@0.1.12",
  "npm:gentle-engram@0.1.14",
  "npm:gentle-engram@0.1.15",
  "npm:gentle-engram@0.1.16",
]);
const HELP = `pi-engram

Usage:
  pi-engram init [--force]

Ensures gentle-engram is declared in settings.json.
Does not create or modify Pi MCP config and does not add an MCP adapter
extension (Pi's built-in MCP reads mcp.json). Install the extension with:
pi install ${PACKAGE_NAME}
`;

function getAgentDir() {
  return process.env.PI_CODING_AGENT_DIR || join(homedir(), ".pi", "agent");
}

function readJsonObject(filePath) {
  if (!existsSync(filePath)) return {};
  const parsed = JSON.parse(readFileSync(filePath, "utf-8"));
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error(`${filePath} must contain a JSON object`);
  }
  return parsed;
}

function writeJsonObject(filePath, data) {
  mkdirSync(dirname(filePath), { recursive: true });
  writeFileSync(filePath, `${JSON.stringify(data, null, 2)}\n`, "utf-8");
}

function ensureEngramPackage(settingsPath) {
  const settings = readJsonObject(settingsPath);
  const packages = Array.isArray(settings.packages) ? settings.packages : [];
  const updatedPackages = [];
  let hasPackage = false;
  let changed = false;

  for (const packageName of packages) {
    if (LEGACY_PACKAGE_NAMES.has(packageName)) {
      changed = true;
      continue;
    }
    if (packageName === PACKAGE_NAME) {
      if (hasPackage) {
        changed = true;
        continue;
      }
      hasPackage = true;
    }
    updatedPackages.push(packageName);
  }

  if (!hasPackage) {
    updatedPackages.push(PACKAGE_NAME);
    changed = true;
  }
  if (changed) {
    settings.packages = updatedPackages;
    writeJsonObject(settingsPath, settings);
  }
  return changed;
}

function warnExistingEngramMcp(mcpPath) {
  const config = readJsonObject(mcpPath);
  if (config.mcpServers && typeof config.mcpServers === "object" && !Array.isArray(config.mcpServers) && Object.hasOwn(config.mcpServers, "engram")) {
    console.warn(`Warning: ${mcpPath} contains mcpServers.engram. Pi native-only agent writes are not guaranteed while this MCP path remains. To use native-only writes, manually remove only mcpServers.engram from ${mcpPath} (preserve other servers), then restart/reload Pi.`);
  }
}

function init() {
  const agentDir = getAgentDir();
  const settingsPath = join(agentDir, "settings.json");
  const mcpPath = join(agentDir, "mcp.json");

  const packageChanged = ensureEngramPackage(settingsPath);
  warnExistingEngramMcp(mcpPath);

  console.log(`Pi agent dir: ${agentDir}`);
  console.log(`${packageChanged ? "Added" : "Kept"} ${PACKAGE_NAME} in settings.json`);
  console.log("Pi-native mem_* tools own agent writes; no Engram MCP registration is created.");
  console.log("Set ENGRAM_URL for an existing engram serve instance, or ENGRAM_BIN for a custom engram binary path.");
}

const command = process.argv[2];
if (command === "init") {
  init();
} else {
  console.log(HELP);
}
