import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

// Regression test for https://github.com/Gentleman-Programming/engram/issues/853
//
// gentle-engram runs inside pi and only ever imports `Text` from
// @earendil-works/pi-tui, yet declared it as a hard `^0.74.0` dependency. That gave
// npm a legal reason to hoist pi-tui 0.74.x to the root of `~/.pi/agent/npm` over the
// `^0.84.x` range declared by the installed pi-coding-agent, crashing every pi child
// spawn with `SyntaxError: ... does not provide an export named 'TuiMainScreen'`.
// Declaring pi-tui as an optional peer dependency lets the host's copy win and keeps
// npm from reintroducing the downgrade.

const pkg = JSON.parse(readFileSync(new URL("../package.json", import.meta.url), "utf8"));
const indexSource = readFileSync(new URL("../index.ts", import.meta.url), "utf8");

const PI_TUI = "@earendil-works/pi-tui";
const PACKAGE_NAME = `npm:${pkg.name}@${pkg.version}`;
const LEGACY_PACKAGE_NAME = "npm:gentle-engram@0.1.8";
const MCP_ADAPTER_PACKAGE = "npm:pi-mcp-adapter";
const CLI_PATH = fileURLToPath(new URL("../cli.js", import.meta.url));
const RELEASE_CONTRACT_PATH = fileURLToPath(new URL("./release-contract.mjs", import.meta.url));
const PUBLISH_WORKFLOW_SOURCE = readFileSync(new URL("../../../.github/workflows/publish-pi.yml", import.meta.url), "utf8");

function runCli(agentDir, ...args) {
	return execFileSync(process.execPath, [CLI_PATH, ...args], {
		encoding: "utf8",
		env: { ...process.env, PI_CODING_AGENT_DIR: agentDir },
	});
}

function readPackages(agentDir) {
	return JSON.parse(readFileSync(join(agentDir, "settings.json"), "utf8")).packages;
}

function runReleaseContract(tag) {
	return spawnSync(process.execPath, [RELEASE_CONTRACT_PATH, tag], { encoding: "utf8" });
}

/**
 * Minimal range check for the peer declaration contract. Supports the range forms the
 * repo actually uses for peer dependencies (`>=x.y.z` and `*`). Anything else throws so
 * a range change here is a conscious decision, not an accident.
 *
 * The reviewed contract (#853) is an OPEN-ENDED >= floor: the plugin only imports
 * `Text` while running inside pi, so it must accept every future pi-tui release
 * (0.85.x, 1.x, 2.x, ...) with no fix required here. The acceptance list below is a
 * floor verification against the two known lines, not a version pin.
 */
function peerRangeAllows(range, version) {
	if (range === "*") return true;
	const match = /^>=(\d+)\.(\d+)\.(\d+)$/.exec(range);
	if (!match) {
		throw new Error(
			`peer range ${JSON.stringify(range)} uses a form this contract test cannot evaluate. ` +
				"The reviewed contract (#853) is an open-ended >= floor, so new pi-tui releases " +
				"keep being accepted without any fix here. If the range change is deliberate " +
				"(e.g. adding an upper bound for a breaking pi-tui major), update peerRangeAllows() " +
				"and the acceptance list in this test consciously.",
		);
	}
	const [major, minor, patch] = version.split(".").map(Number);
	const [floorMajor, floorMinor, floorPatch] = match.slice(1).map(Number);
	if (major !== floorMajor) return major > floorMajor;
	if (minor !== floorMinor) return minor > floorMinor;
	return patch >= floorPatch;
}

test("pi-tui is not a hard dependency", () => {
	assert.equal(
		pkg.dependencies?.[PI_TUI],
		undefined,
		"pi-tui must not be declared in dependencies: a hard dep gives npm a reason to hoist a 0.74.x over the host's ^0.84.x and crash pi startup",
	);
	assert.equal(
		pkg.optionalDependencies?.[PI_TUI],
		undefined,
		"pi-tui must not be declared in optionalDependencies either: npm installs optional dependencies by default, so this door would reintroduce the same downgrade",
	);
});

test("pi-tui is declared as an optional peer dependency", () => {
	const range = pkg.peerDependencies?.[PI_TUI];
	assert.ok(range, "pi-tui must be declared in peerDependencies so the host pi installation's copy is used");
	assert.equal(
		pkg.peerDependenciesMeta?.[PI_TUI]?.optional,
		true,
		"the peer must be optional so npm never auto-installs a second pi-tui into pi-managed trees",
	);
});

test("pi-tui peer range accepts every pi-tui line the plugin renders against", () => {
	const range = pkg.peerDependencies?.[PI_TUI];
	assert.ok(range, "peerDependencies must declare a pi-tui range");
	// `Text` is the only export used and exists across the 0.74 -> 0.84 lines; a caret
	// range on 0.x would pin the minor and conflict with the host's 0.84.x.
	for (const version of ["0.74.0", "0.74.2", "0.84.3"]) {
		assert.ok(
			peerRangeAllows(range, version),
			`peer range ${JSON.stringify(range)} must accept pi-tui ${version} (host ships 0.84.x, plugin is verified against 0.74.x)`,
		);
	}
	// Pin the lower bound, not just acceptance: a wider range (`*`, `>=0.0.0`) would
	// silently bless pi-tui lines the plugin has never rendered against. Note `*` is
	// accepted by the helper but rejected here on purpose: `*` keeps accepting future
	// releases, yet it would also bless a 0.x line below the verified 0.74 floor.
	assert.equal(
		peerRangeAllows(range, "0.73.9"),
		false,
		`peer range ${JSON.stringify(range)} must reject pi-tui 0.73.9: the >=0.74.0 floor is the reviewed contract`,
	);
});

test("the peer declaration stays justified by actual pi-tui usage", () => {
	assert.match(
		indexSource,
		/from "@earendil-works\/pi-tui"/,
		"index.ts imports pi-tui; if that import is ever removed, drop the peer dependency too",
	);
});

test("pi-engram init adds the current package and help names its install command", () => {
	const agentDir = mkdtempSync(join(tmpdir(), "engram-pi-cli-"));
	try {
		const output = runCli(agentDir, "init");
		assert.deepEqual(readPackages(agentDir), [MCP_ADAPTER_PACKAGE, PACKAGE_NAME]);
		assert.match(output, new RegExp(`Added ${PACKAGE_NAME} in settings\\.json`));
		assert.match(runCli(agentDir), new RegExp(`pi install ${PACKAGE_NAME}`));
	} finally {
		rmSync(agentDir, { recursive: true, force: true });
	}
});

test("pi-engram init replaces legacy package entries without disturbing other packages", () => {
	const agentDir = mkdtempSync(join(tmpdir(), "engram-pi-cli-"));
	try {
		writeFileSync(
			join(agentDir, "settings.json"),
			JSON.stringify({ packages: ["npm:existing", LEGACY_PACKAGE_NAME, PACKAGE_NAME, LEGACY_PACKAGE_NAME, MCP_ADAPTER_PACKAGE] }),
		);

		const output = runCli(agentDir, "init");
		assert.deepEqual(readPackages(agentDir), ["npm:existing", PACKAGE_NAME, MCP_ADAPTER_PACKAGE]);
		assert.match(output, new RegExp(`Added ${PACKAGE_NAME} in settings\\.json`));

		const settingsAfterMigration = readFileSync(join(agentDir, "settings.json"), "utf8");
		const repeatOutput = runCli(agentDir, "init");
		assert.equal(readFileSync(join(agentDir, "settings.json"), "utf8"), settingsAfterMigration);
		assert.match(repeatOutput, new RegExp(`Kept ${PACKAGE_NAME} in settings\\.json`));
	} finally {
		rmSync(agentDir, { recursive: true, force: true });
	}
});

test("Pi release contract accepts the package version tag and CLI guidance", () => {
	const result = runReleaseContract(`pi-v${pkg.version}`);
	assert.equal(result.status, 0, result.stderr);
});

test("Pi release contract rejects a tag that differs from the package version", () => {
	const result = runReleaseContract("pi-v0.0.0");
	assert.notEqual(result.status, 0);
	assert.match(result.stderr, /must match package version/);
});

test("Pi publish workflow passes the release tag through a shell environment variable", () => {
	assert.match(
		PUBLISH_WORKFLOW_SOURCE,
		/env:\s*\n\s+RELEASE_TAG:\s*\$\{\{\s*github\.ref_name\s*\}\}\s*\n\s+run:\s*node test\/release-contract\.mjs "\$RELEASE_TAG"/,
		"the release tag must be provided as RELEASE_TAG and quoted when passed to the release contract",
	);
	assert.doesNotMatch(
		PUBLISH_WORKFLOW_SOURCE,
		/run:\s*node test\/release-contract\.mjs\s+[^\n]*\$\{\{/,
		"the release contract shell command must not interpolate a GitHub expression directly",
	);
});
