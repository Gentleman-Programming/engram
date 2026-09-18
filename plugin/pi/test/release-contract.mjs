import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const packageMetadata = JSON.parse(readFileSync(new URL("../package.json", import.meta.url), "utf8"));
const installerSource = readFileSync(new URL("../../../internal/setup/setup.go", import.meta.url), "utf8");
const installerPinMatches = [...installerSource.matchAll(/^\s*piGentleEngramPackage\s*=\s*"npm:gentle-engram@([^"]+)"\s*$/gm)];
const packageName = `npm:${packageMetadata.name}@${packageMetadata.version}`;
const expectedTag = `pi-v${packageMetadata.version}`;

if (installerPinMatches.length === 0) {
	throw new Error("Pi installer pin npm:gentle-engram@<version> is missing from internal/setup/setup.go");
}
if (installerPinMatches.length !== 1) {
	throw new Error("Pi installer pin npm:gentle-engram@<version> is ambiguous in internal/setup/setup.go");
}

const installerVersion = installerPinMatches[0][1];
if (installerVersion !== packageMetadata.version) {
	throw new Error(`Pi installer pin npm:gentle-engram@${installerVersion} must match plugin/pi/package.json version ${packageMetadata.version}`);
}
const releaseTag = process.argv[2];
const cliPath = fileURLToPath(new URL("../cli.js", import.meta.url));

if (releaseTag !== expectedTag) {
	throw new Error(`Pi release tag ${JSON.stringify(releaseTag)} must match package version ${expectedTag}`);
}

const help = execFileSync(process.execPath, [cliPath], { encoding: "utf8" });
if (!help.includes(`pi install ${packageName}`)) {
	throw new Error(`Pi CLI help must include the current package ${packageName}`);
}
