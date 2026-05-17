#!/usr/bin/env node
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, join } from "node:path";

const PACKAGE_NAME = "npm:gentle-engram@0.1.5";
const MCP_ADAPTER_PACKAGE = "npm:pi-mcp-adapter";
const HELP = `pi-engram

Usage:
  pi-engram init [--force]

Creates Pi's Engram MCP config in the Pi agent dir and ensures pi-mcp-adapter
is declared in settings.json. The Pi extension itself is loaded by installing
the package with: pi install npm:gentle-engram@0.1.5
`;

const MCP_LAUNCHER =
  "const { spawn } = require('node:child_process'); const bin = process.env.ENGRAM_BIN || 'engram'; const child = spawn(bin, ['mcp', '--tools=agent'], { stdio: 'inherit' }); child.on('error', () => process.exit(127)); child.on('exit', (code, signal) => { if (typeof code === 'number') process.exit(code); process.kill(process.pid, signal || 'SIGTERM'); });";

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

function ensurePackage(settingsPath, packageName) {
  const settings = readJsonObject(settingsPath);
  const packages = Array.isArray(settings.packages) ? settings.packages : [];
  if (!packages.includes(packageName)) {
    settings.packages = [...packages, packageName];
    writeJsonObject(settingsPath, settings);
    return true;
  }
  return false;
}

function createEngramServerConfig() {
  return {
    command: "node",
    args: ["-e", MCP_LAUNCHER],
    lifecycle: "lazy",
    directTools: false,
  };
}

function ensureMcpConfig(mcpPath, force) {
  const config = readJsonObject(mcpPath);
  const existingServers = config.mcpServers && typeof config.mcpServers === "object" && !Array.isArray(config.mcpServers)
    ? config.mcpServers
    : {};

  if (existingServers.engram && !force) {
    return false;
  }

  config.mcpServers = {
    ...existingServers,
    engram: createEngramServerConfig(),
  };
  writeJsonObject(mcpPath, config);
  return true;
}

function init() {
  const force = process.argv.includes("--force");
  const agentDir = getAgentDir();
  const settingsPath = join(agentDir, "settings.json");
  const mcpPath = join(agentDir, "mcp.json");

  const adapterChanged = ensurePackage(settingsPath, MCP_ADAPTER_PACKAGE);
  const packageChanged = ensurePackage(settingsPath, PACKAGE_NAME);
  const mcpChanged = ensureMcpConfig(mcpPath, force);

  console.log(`Pi agent dir: ${agentDir}`);
  console.log(`${adapterChanged ? "Added" : "Kept"} ${MCP_ADAPTER_PACKAGE} in settings.json`);
  console.log(`${packageChanged ? "Added" : "Kept"} ${PACKAGE_NAME} in settings.json`);
  console.log(`${mcpChanged ? "Wrote" : "Kept existing"} Engram MCP server in mcp.json`);
  console.log("Set ENGRAM_URL for an existing engram serve instance, or ENGRAM_BIN for a custom engram binary path.");
}

const command = process.argv[2];
if (command === "init") {
  init();
} else {
  console.log(HELP);
}
