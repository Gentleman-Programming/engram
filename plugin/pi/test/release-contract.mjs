import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const packageMetadata = JSON.parse(readFileSync(new URL("../package.json", import.meta.url), "utf8"));
const packageName = `npm:${packageMetadata.name}@${packageMetadata.version}`;
const expectedTag = `pi-v${packageMetadata.version}`;
const releaseTag = process.argv[2];
const cliPath = fileURLToPath(new URL("../cli.js", import.meta.url));

if (releaseTag !== expectedTag) {
	throw new Error(`Pi release tag ${JSON.stringify(releaseTag)} must match package version ${expectedTag}`);
}

const help = execFileSync(process.execPath, [cliPath], { encoding: "utf8" });
if (!help.includes(`pi install ${packageName}`)) {
	throw new Error(`Pi CLI help must include the current package ${packageName}`);
}
