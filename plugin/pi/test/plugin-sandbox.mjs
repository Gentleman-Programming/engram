// Loading `index.ts` needs its two runtime dependencies resolvable, and the suite must not
// get them by writing into the checkout's real `node_modules`: that replaces the installed
// packages for every later build, test, or manual run in the same clone, and makes a test's
// result depend on whether it had already run. Instead every fixture copies the plugin into a
// throwaway directory that owns its own `node_modules`, and deletes the whole directory after.
import { copyFile, mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const ROOT = dirname(dirname(fileURLToPath(import.meta.url)));

// Everything the extension entry point pulls in at runtime. `@earendil-works/pi-coding-agent`
// is a type-only import and is erased, so only the two value imports need runtime stubs.
const PLUGIN_SOURCES = ["index.ts", "compaction-recovery.js", "memory-tool-chrome.js", "private-redaction.js", "runtime-contract.js", "runtime-contract.json"];

// Stamped into every stub so a test can prove none of them ever reached the real node_modules.
export const RUNTIME_STUB_MARKER = "engram-pi test runtime stub";

export const PLUGIN_ROOT = ROOT;

export async function createPluginSandbox(parent, { controlledDelay } = {}) {
  const sandbox = join(parent, "plugin");
  await mkdir(sandbox, { recursive: true });
  for (const file of PLUGIN_SOURCES) {
    await copyFile(join(ROOT, file), join(sandbox, file));
  }

  // Lexical timer ownership belongs to this copied module, never to process globals.
  await writeFile(join(sandbox, "sandbox-timers.mjs"), `
const pending = new Map();
const controlledDelay = ${JSON.stringify(controlledDelay ?? null)};
export function setTimeout(callback, delay, ...args) {
  let handle;
  const run = () => { pending.delete(handle); callback(...args); };
  handle = delay === controlledDelay ? { unref() { return this; } } : globalThis.setTimeout(run, delay);
  pending.set(handle, { delay, run });
  return handle;
}
export function clearTimeout(handle) {
  pending.delete(handle);
  globalThis.clearTimeout(handle);
}
export const clock = {
  pending: () => [...pending.values()].filter(({ delay }) => delay === controlledDelay).length,
  async advance() {
    for (const { delay, run } of [...pending.values()]) if (delay === controlledDelay) run();
    await new Promise((resolve) => globalThis.setImmediate(resolve));
  },
  dispose() {
    for (const handle of pending.keys()) clearTimeout(handle);
  },
};
`);
  const entry = join(sandbox, "index.ts");
  await writeFile(entry, 'import { setTimeout, clearTimeout } from "./sandbox-timers.mjs";\n' + await readFile(entry, "utf8"));

  const modules = join(sandbox, "node_modules");
  await mkdir(join(modules, "@earendil-works", "pi-tui"), { recursive: true });
  await writeFile(
    join(modules, "@earendil-works", "pi-tui", "package.json"),
    JSON.stringify({ type: "module", exports: "./index.js" }),
  );
  await writeFile(
    join(modules, "@earendil-works", "pi-tui", "index.js"),
    `// ${RUNTIME_STUB_MARKER}
export class Text { constructor(text) { this.text = text; } }
`,
  );

  await mkdir(join(modules, "typebox"), { recursive: true });
  await writeFile(
    join(modules, "typebox", "package.json"),
    JSON.stringify({ type: "module", exports: "./index.js" }),
  );
  await writeFile(
    join(modules, "typebox", "index.js"),
    `// ${RUNTIME_STUB_MARKER}
const schema = (kind) => (...args) => ({ kind, args });
export const Type = new Proxy({}, { get: (_target, prop) => schema(String(prop)) });
`,
  );
  return sandbox;
}

// Each call gets a fresh sandbox path, so the import specifier is already unique per fixture
// and the module graph is fresh without a cache-busting query string.
export async function importPluginFromSandbox(sandbox) {
  const { default: registerEngram } = await import(pathToFileURL(join(sandbox, "index.ts")).href);
  return registerEngram;
}

// Runs `body` against a sandbox rooted in its own temp directory, cancels its pending
// timeouts, then removes the directory. Cancellation does not resolve waits or run callbacks,
// so retired modules cannot resume delayed work against the next fixture's globals.
// Nothing under the checkout is created, replaced, or deleted.
export async function withPluginSandbox(prefix, body, options) {
  const dir = await mkdtemp(join(tmpdir(), prefix));
  let clock;
  try {
    const sandbox = await createPluginSandbox(dir, options);
    ({ clock } = await import(pathToFileURL(join(sandbox, "sandbox-timers.mjs")).href));
    return await body({ dir, sandbox, clock });
  } finally {
    clock?.dispose();
    await rm(dir, { recursive: true, force: true });
  }
}
