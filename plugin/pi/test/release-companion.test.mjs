import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { test } from "node:test";

const root = new URL("../../../", import.meta.url);
const helper = new URL(".github/scripts/release-companion.mjs", root);
const pkg = { name: "gentle-engram", version: "0.4.2", engram: { startupPolicy: 1, sessionOwned: 1 } };
const pin = 'const piGentleEngramPackage = "npm:gentle-engram@0.4.2"';
const paths = ["plugin/pi/package.json", "internal/setup/setup.go", ".github/workflows/release.yml"];
const before = paths.map(p => readFileSync(new URL(p, root), "utf8"));

function run({ source = pkg, setup = pin, published = pkg, status = 200, failure, malformed = false, args = [] } = {}) {
	const preload = `import fs from 'node:fs'; import {syncBuiltinESMExports} from 'node:module';
const source=${JSON.stringify(source)}, setup=${JSON.stringify(setup)};
const original=fs.readFileSync;
fs.readFileSync=function(path,...rest){ if(String(path).endsWith('plugin/pi/package.json')) return JSON.stringify(source); if(String(path).endsWith('internal/setup/setup.go')) return setup; return original.call(this,path,...rest); }; syncBuiltinESMExports();
let calls=0; process.on('exit',()=>console.error('TRANSPORT '+JSON.stringify({calls})));
globalThis.fetch=async(url,options)=>{calls++; console.error('GET '+JSON.stringify({url, method:options.method, redirect:options.redirect, credentials:options.credentials, headers:options.headers, bounded:options.signal instanceof AbortSignal})); ${failure ? `throw new Error(${JSON.stringify(failure)});` : `return {ok:${status === 200},status:${status},json:async()=>{${malformed ? "throw new SyntaxError('bad JSON')" : `return ${JSON.stringify(published)}`}}};`} };
`;
	const result = spawnSync(process.execPath, ["--import", `data:text/javascript,${encodeURIComponent(preload)}`, helper.pathname.replace(/^\/(?:([A-Za-z]:))/, "$1"), ...args], { cwd: root, encoding: "utf8", timeout: 5000, env: { ...process.env, NODE_OPTIONS: "" } });
	assert.equal(result.error, undefined);
	assert.deepEqual(paths.map(p => readFileSync(new URL(p, root), "utf8")), before, "CLI must not change source/artifacts");
	return result;
}

for (const owned of [true, false]) test(`exact public metadata succeeds (sessionOwned declared: ${owned})`, () => {
	const source = structuredClone(pkg);
	if (!owned) delete source.engram.sessionOwned;
	const result = run({ source, published: source });
	assert.equal(result.status, 0, result.stderr);
	assert.match(result.stdout, /Companion available: gentle-engram@0.4.2/);
	assert.match(result.stderr, /TRANSPORT {"calls":1}/);
	const request = JSON.parse(result.stderr.split("\n").find(l => l.startsWith("GET ")).slice(4));
	assert.deepEqual(request, { url: "https://registry.npmjs.org/gentle-engram/0.4.2", method: "GET", redirect: "error", credentials: "omit", headers: { Accept: "application/json" }, bounded: true });
});

for (const [label, options, calls] of [
	["404", { status: 404 }, 1], ["HTTP", { status: 503 }, 1],
	["network", { failure: "network unavailable" }, 1], ["redirect", { failure: "redirect forbidden" }, 1],
	["timeout", { failure: "timeout" }, 1], ["JSON", { malformed: true }, 1],
	["name", { published: { ...pkg, name: "other" } }, 1],
	["version", { published: { ...pkg, version: "0.4.1" } }, 1],
	["public 0.4.2 lacks owned metadata", { published: { ...pkg, engram: { startupPolicy: 1 } } }, 1],
	["startup", { published: { ...pkg, engram: { startupPolicy: 2, sessionOwned: 1 } } }, 1],
	["null", { published: null }, 1],
	["pin", { setup: pin.replace("0.4.2", "0.4.1") }, 0],
	["legacy pin only", { setup: pin.replace("piGentleEngramPackage", "piLegacyGentleEngramPackage") }, 0],
	["unsafe version", { source: { ...pkg, version: "../latest" } }, 0],
	["wrong source name", { source: { ...pkg, name: "other" } }, 0],
	["missing declaration", { source: { ...pkg, engram: {} } }, 0],
	["invalid owned declaration", { source: { ...pkg, engram: { startupPolicy: 1, sessionOwned: 2 } } }, 0],
	["endpoint argument", { args: ["https://example.com"] }, 0],
]) test(`rejects ${label} without retry or writes`, () => {
	const result = run(options);
	assert.equal(result.status, 1, result.stderr);
	assert.equal(result.stdout, "");
	assert.match(result.stderr, /Companion preflight failed:/);
	assert.match(result.stderr, new RegExp(`TRANSPORT {"calls":${calls}}`));
});

test("core guard is blocking before GoReleaser with pinned Node", () => {
	const workflow = readFileSync(new URL(".github/workflows/release.yml", root), "utf8").replaceAll("\r\n", "\n");
	assert.match(workflow, /uses: actions\/setup-node@a0853c24544627f65ddf259abe73b1d18a591444 # v5\n        with:\n          node-version: "24"/);
	assert.match(workflow, /      - name: Verify published Pi companion\n        run: node .github\/scripts\/release-companion.mjs\n\n      - name: Run GoReleaser/);
});
