import { readFileSync } from "node:fs";

try {
	if (process.argv.length !== 2) throw new Error("No arguments accepted");
	const pkg = JSON.parse(readFileSync("plugin/pi/package.json", "utf8"));
	const setup = readFileSync("internal/setup/setup.go", "utf8");
	const pins = [...setup.matchAll(/\bconst\s+piGentleEngramPackage\s*=\s*"([^"]+)"|^\s*piGentleEngramPackage\s*=\s*"([^"]+)"/gm)];
	const version = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/;
	if (pkg.name !== "gentle-engram" || typeof pkg.version !== "string" || !version.test(pkg.version)) {
		throw new Error("Invalid companion package name/version");
	}
	if (pins.length !== 1 || (pins[0][1] ?? pins[0][2]) !== `npm:${pkg.name}@${pkg.version}`) {
		throw new Error("Actual installer companion pin must match package name/version");
	}
	if (pkg.engram?.startupPolicy !== 1 ||
		(Object.hasOwn(pkg.engram, "sessionOwned") && pkg.engram.sessionOwned !== 1)) {
		throw new Error("Invalid declared companion metadata");
	}
	const response = await fetch(`https://registry.npmjs.org/gentle-engram/${encodeURIComponent(pkg.version)}`, {
		method: "GET",
		redirect: "error",
		credentials: "omit",
		headers: { Accept: "application/json" },
		signal: AbortSignal.timeout(10000),
	});
	if (!response.ok) throw new Error(`Public registry HTTP ${response.status}`);
	const published = await response.json();
	if (published?.name !== pkg.name || published?.version !== pkg.version ||
		published?.engram?.startupPolicy !== pkg.engram.startupPolicy ||
		(Object.hasOwn(pkg.engram, "sessionOwned") && published?.engram?.sessionOwned !== pkg.engram.sessionOwned)) {
		throw new Error("Public companion metadata does not match required declarations");
	}
	console.log(`Companion available: ${pkg.name}@${pkg.version}`);
} catch (error) {
	console.error(`Companion preflight failed: ${error.message}`);
	process.exitCode = 1;
}
