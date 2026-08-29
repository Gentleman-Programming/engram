import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

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

/**
 * Minimal range check for the peer declaration contract. Supports the range forms the
 * repo actually uses for peer dependencies (`>=x.y.z` and `*`). Anything else fails
 * the test loudly so a range change here is a conscious decision, not an accident.
 */
function peerRangeAllows(range, version) {
	if (range === "*") return true;
	const match = /^>=(\d+)\.(\d+)\.(\d+)$/.exec(range);
	if (!match) return false;
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
	for (const version of ["0.74.2", "0.84.3"]) {
		assert.ok(
			peerRangeAllows(range, version),
			`peer range ${JSON.stringify(range)} must accept pi-tui ${version} (host ships 0.84.x, plugin is verified against 0.74.x)`,
		);
	}
});

test("the peer declaration stays justified by actual pi-tui usage", () => {
	assert.match(
		indexSource,
		/from "@earendil-works\/pi-tui"/,
		"index.ts imports pi-tui; if that import is ever removed, drop the peer dependency too",
	);
});
