import { readFile } from "node:fs/promises";
import { pathToFileURL } from "node:url";
import { withPluginSandbox, importPluginFromSandbox } from "../plugin/pi/test/plugin-sandbox.mjs";

const help = "Usage: node tools/pi-schema-diagnostic.mjs [--help]\nDiagnostic only; not parity or release approval.\nExit 0: no structural differences (or help); 1: differences; 2: failure.\n";
const object = (value) => value !== null && typeof value === "object" && !Array.isArray(value);
const validName = (name) => typeof name === "string" && /^mem_[a-z0-9_]{1,80}$/.test(name);
const escape = (key) => key.replaceAll("~", "~0").replaceAll("/", "~1");
const display = (value) => value === undefined ? "<missing>" : JSON.stringify(value);

// Compare serialized JSON, not a homegrown TypeBox converter or semantic normalizer.
// Object key ordering alone is ignored; arrays retain their exact order.
export function compareSchemas(contract, registrations) {
  if (!object(contract?.operations) || !Object.keys(contract.operations).length || !Array.isArray(registrations) || !registrations.length) throw new Error("Invalid capture or baseline");
  const operations = contract.operations;
  if (Object.entries(operations).some(([name, operation]) => !validName(name) || !object(operation?.input_schema))) throw new Error("Invalid baseline");
  const tools = registrations.map((tool) => {
    if (!validName(tool?.name) || !object(tool?.parameters)) throw new Error("Invalid registration");
    const schema = JSON.parse(JSON.stringify(tool.parameters));
    if (!object(schema)) throw new Error("Invalid serialized schema");
    return { name: tool.name, schema };
  });
  const lines = [];
  function walk(name, path, go, pi) {
    if (object(go) && object(pi)) {
      for (const key of [...new Set([...Object.keys(go), ...Object.keys(pi)])].sort()) walk(name, `${path}/${escape(key)}`, go[key], pi[key]);
    } else if (Array.isArray(go) && Array.isArray(pi)) {
      for (let i = 0; i < Math.max(go.length, pi.length); i++) walk(name, `${path}/${i}`, go[i], pi[i]);
    } else if (go !== pi) lines.push(`${name} ${path}: Go=${display(go)} Pi=${display(pi)}`);
  }
  for (const name of [...new Set([...Object.keys(operations), ...tools.map((tool) => tool.name)])].sort()) {
    const matches = tools.filter((tool) => tool.name === name);
    if (matches.length > 1) lines.push(`${name} /registrations: duplicate count=${matches.length}`);
    if (!operations[name]) lines.push(`${name} /registrations: Go=<missing> Pi=${matches.length}`);
    else if (!matches.length) lines.push(`${name} /registrations: Go=1 Pi=<missing>`);
    // Preserve and compare every duplicate, never overwrite a registration in a map.
    matches.forEach((tool, index) => {
      if (operations[name]) walk(name, matches.length > 1 ? `/registrations/${index}/input_schema` : "/input_schema", operations[name].input_schema, tool.schema);
    });
  }
  return lines;
}

export async function diagnose() {
  const contract = JSON.parse(await readFile(new URL("../docs/contracts/agent-contract-v1.json", import.meta.url), "utf8"));
  if (contract.contract_id !== "engram.agent-contract/v1" || contract.revision !== 1 || contract.profile !== "agent") throw new Error("Invalid baseline");
  // Return only after timer disposal and directory cleanup have both completed.
  return withPluginSandbox("engram-pi-schema-", async ({ sandbox }) => {
    const registrations = [];
    const register = await importPluginFromSandbox(sandbox);
    register({ registerTool(tool) { registrations.push(tool); }, on() {} });
    return compareSchemas(contract, registrations);
  }, { realTypeBox: true });
}

async function main(args) {
  if (args.length === 1 && args[0] === "--help") return process.stdout.write(help);
  if (args.length) {
    process.stderr.write("Pi schema diagnostic failed: unsupported arguments.\n");
    process.exitCode = 2;
    return;
  }
  try {
    const lines = await diagnose();
    process.stdout.write(`Diagnostic only; not parity or release approval.\nStructural differences: ${lines.length}\n${lines.length ? lines.join("\n") + "\n" : ""}`);
    process.exitCode = lines.length ? 1 : 0;
  } catch {
    // Never expose imported exception messages, private paths, or partial diagnostics.
    process.stderr.write("Pi schema diagnostic failed: baseline, real TypeBox, capture or cleanup unavailable.\n");
    process.exitCode = 2;
  }
}
if (process.argv[1] && pathToFileURL(process.argv[1]).href === import.meta.url) await main(process.argv.slice(2));
