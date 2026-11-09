import { spawnSync } from "node:child_process";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import { join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { createPluginSandbox, importPluginFromSandbox } from "../plugin/pi/test/plugin-sandbox.mjs";

const root = fileURLToPath(new URL("../", import.meta.url));
const help = `Usage: node tools/release-candidate.mjs --core-ref refs/tags/vSEMVER --pi-ref refs/tags/pi-vSEMVER

Full refs define an ordered pair of independent strict SemVer versions.
Go JSON version means core; piVersion means Pi. Identity is metadata only,
not remote tag/source/digest verification or compatibility proof.
Rejection-only checker: validates metadata through the live Go exporter and
unchanged Pi release guard, then captures actual sandbox tool registrations.
The sandbox TypeBox stub is registration-only, not JSON-schema equivalence proof.
Inventory equality is not semantic parity or release approval. Unknown schema
and semantic proof always block; mcp-foundation and declared Pi differences block.
Candidate checks always exit nonzero and emit no success JSON. Only --help exits 0.
Full Go/Pi suite orchestration and npm pack/tarball checks are deferred.
No publication, network, version/tag changes, bypass or override flags.
`;

class Rejection extends Error {}

function reject(message) {
  throw new Rejection(message);
}

function argumentsFor(args) {
  if (args.length === 1 && args[0] === "--help") return null;
  const refs = {};
  for (let i = 0; i < args.length; i += 2) {
    const flag = args[i];
    if (!["--core-ref", "--pi-ref"].includes(flag)) reject("Unsupported option or positional argument.");
    if (refs[flag] !== undefined) reject("Duplicate ref option is unsupported.");
    const value = args[i + 1];
    if (!value || value.startsWith("--")) reject("core-ref and pi-ref values are required.");
    refs[flag] = value;
  }
  if (!refs["--core-ref"] || !refs["--pi-ref"]) reject("core-ref and pi-ref are required.");
  return refs;
}

// Never forward raw compiler, provider, filesystem or guard diagnostics. Only
// fixed validator categories cross the public CLI boundary.
function goFailure(stderr) {
  if (/invalid core-ref/.test(stderr)) return "Invalid core-ref: expected a full strict SemVer ref.";
  if (/invalid pi-ref/.test(stderr)) return "Invalid pi-ref: expected a full strict SemVer ref.";
  if (/package name must be gentle-engram/.test(stderr)) return "Package name must be gentle-engram.";
  if (/package version must match candidate version/.test(stderr)) return "Package version must match candidate version.";
  if (/installer pin must match candidate version exactly/.test(stderr)) return "Installer pin must match candidate version exactly.";
  return "Go validator/exporter failed or is unavailable; candidate rejected.";
}

function subprocess(executable, args, env, label, timeout) {
  const result = spawnSync(executable, args, {
    cwd: root, env, shell: false, encoding: "utf8", timeout, maxBuffer: 1024 * 1024,
  });
  if (result.error || result.signal) reject(`${label} failed or is unavailable; candidate rejected.`);
  return result;
}

function toolNames(names) {
  if (names.length > 200 || names.some((name) => typeof name !== "string" || !/^mem_[a-z0-9_]{1,80}$/.test(name))) {
    reject("Unknown or malformed tool inventory; candidate rejected.");
  }
  return names;
}

async function check(refs) {
  // Own all disposable files under this checkout, even without caller TMP vars.
  // Existing Go caches remain caller-owned; no cache installation or clearing.
  const temporary = await mkdtemp(join(root, ".release-candidate-"));
  let clock;
  try {
    const temp = join(temporary, "tmp");
    await mkdir(temp);
    const env = {
      ...process.env, TEMP: temp, TMP: temp, TMPDIR: temp, NODE_OPTIONS: "",
      GOPROXY: "off", GOTOOLCHAIN: "local", GOSUMDB: "off", GOFLAGS: "-mod=readonly",
    };
    const exported = subprocess("go", ["run", "./tools/release-candidate", "-core-ref", refs["--core-ref"], "-pi-ref", refs["--pi-ref"]], env, "Go validator/exporter", 24_000);
    if (exported.status !== 0) reject(goFailure(exported.stderr));
    let document;
    try { document = JSON.parse(exported.stdout); } catch { reject("Malformed Go contract export; candidate rejected."); }
    const contract = document?.contract;
    if (typeof document?.version !== "string" || typeof document?.piVersion !== "string" || document.version !== refs["--core-ref"].slice("refs/tags/v".length) || document.piVersion !== refs["--pi-ref"].slice("refs/tags/pi-v".length) || contract?.contract_id !== "engram.agent-contract/v1" || contract.revision !== 1 || contract.profile !== "agent" || !contract.operations || typeof contract.operations !== "object" || Array.isArray(contract.operations)) {
      reject("Unknown or malformed Go contract export; candidate rejected.");
    }
    const goNames = toolNames(Object.keys(contract.operations)).sort();
    const guard = subprocess(process.execPath, [join(root, "plugin/pi/test/release-contract.mjs"), refs["--pi-ref"]], env, "Pi release guard", 3_000);
    if (guard.status !== 0) reject("Pi release guard rejected candidate metadata.");

    const sandbox = await createPluginSandbox(temporary);
    ({ clock } = await import(pathToFileURL(join(sandbox, "sandbox-timers.mjs")).href));
    const registrations = [];
    const register = await importPluginFromSandbox(sandbox);
    register({ registerTool(tool) { registrations.push(tool); }, on() {} });
    const piNames = toolNames(registrations.map((tool) => tool?.name));
    const duplicates = [...new Set(piNames.filter((name, index) => piNames.indexOf(name) !== index))].sort();
    const goOnly = goNames.filter((name) => !piNames.includes(name));
    const piOnly = [...new Set(piNames.filter((name) => !goNames.includes(name)))].sort();
    const differences = goNames.filter((name) => {
      const declared = contract.operations[name]?.semantics?.pi_differences;
      return Array.isArray(declared) && declared.length > 0;
    });
    const blockers = ["Candidate rejected (rejection-only checker; no release approval)."];
    if (duplicates.length) blockers.push(`Duplicate Pi registrations: ${duplicates.join(", ")}.`);
    if (goOnly.length) blockers.push(`Inventory Go-only: ${goOnly.join(", ")}.`);
    if (piOnly.length) blockers.push(`Inventory Pi-only: ${piOnly.join(", ")}.`);
    if (contract.status === "mcp-foundation") blockers.push("Go contract is mcp-foundation, not cross-runtime semantic parity.");
    else blockers.push("Go contract semantic readiness is unknown; no status establishes approval in this slice.");
    if (differences.length) blockers.push(`Declared Pi semantic differences: ${differences.join(", ")}.`);
    blockers.push("Sandbox TypeBox stub captures registration only; JSON-schema equivalence is unproven.");
    blockers.push("Genuine Pi semantic proof is unavailable; even matching inventories remain rejected.");
    blockers.push("Full suite orchestration and pack/tarball validation are deferred, not passed.");
    return blockers.join("\n");
  } finally {
    clock?.dispose();
    await rm(temporary, { recursive: true, force: true });
  }
}

let diagnostic;
try {
  const refs = argumentsFor(process.argv.slice(2));
  if (refs === null) process.stdout.write(help);
  else {
    diagnostic = await check(refs);
    process.exitCode = 1;
  }
} catch (error) {
  // Only fixed messages authored here may be exposed; imported runtime failures
  // can contain private paths or provider output and receive a generic category.
  diagnostic = error instanceof Rejection
    ? error.message : "Candidate checker failed; runtime capture or temporary cleanup unavailable.";
  process.exitCode = 1;
}
if (diagnostic) process.stderr.write(`${diagnostic}\n`);
