import { fileURLToPath, pathToFileURL } from "node:url";
import { existsSync, realpathSync } from "node:fs";
import { dirname, isAbsolute, relative, sep } from "node:path";
import { registerHooks } from "node:module";
// Fixed native module bootstrap, not Pi registration or installed-SDK execution.
let bootstrapped = false, parentGone = false;
process.stdin.on("end", () => { parentGone = true; if (bootstrapped) process.exit(0); });
process.stdin.resume();
try {
  if (Number(process.versions.node.split(".")[0]) !== 24) throw new Error("prerequisite");
  const response = await fetch(new URL("/health", process.env.ENGRAM_URL), {
    headers: { Authorization: `Bearer ${process.env.ENGRAM_TOKEN}` },
    signal: AbortSignal.timeout(2000), redirect: "error",
  });
  let body = "";
  for await (const chunk of response.body) {
    body += Buffer.from(chunk).toString();
    if (Buffer.byteLength(body) > 4096) throw new Error("health limit");
  }
  const health = JSON.parse(body);
  if (response.status !== 200 || health.status !== "ok" || health.service !== "engram" || health.instance_id !== process.env.ENGRAM_EXPECTED_INSTANCE) throw new Error("identity");
  const root = realpathSync(dirname(process.argv[2]));
  const peerURL = new URL("./plugin/pi/node_modules/typebox/", import.meta.url);
  const peerRoot = existsSync(peerURL) ? realpathSync(peerURL) : undefined;
  const anchor = new URL("./plugin/pi/fixed-owner.mjs", import.meta.url).href;
  // Fixed, disclosed UI-only substitute. No SDK host or rendering is exercised.
  const textUI = "data:text/javascript," + encodeURIComponent("export class Text { constructor(text) { this.text = String(text); } }");
  let uiUsed = false;
  // Import boundary only: reviewed builtins still permit filesystem/process access.
  registerHooks({ resolve(specifier, context, nextResolve) {
    if (specifier === "@earendil-works/pi-tui") {
      uiUsed = true;
      return { url: textUI, shortCircuit: true };
    }
    const resolved = nextResolve(specifier, specifier === "typebox" && peerRoot ? { ...context, parentURL: anchor } : context);
    if (resolved.url.startsWith("node:")) return resolved;
    if (!resolved.url.startsWith("file:")) throw new Error("module boundary");
    const file = realpathSync(fileURLToPath(resolved.url));
    const allowed = [root, peerRoot].filter(Boolean).some(base => {
      const location = relative(base, file);
      return !isAbsolute(location) && location !== ".." && !location.startsWith(`..${sep}`);
    });
    if (!allowed) throw new Error("module boundary");
    return resolved;
  } });
  await import(pathToFileURL(process.argv[2]).href);
  if (parentGone) process.exit(1);
  bootstrapped = true;
  process.send({ protocol: "engram-pi-bootstrap-v1", substitutions: uiUsed ? ["@earendil-works/pi-tui.Text"] : [] });
} catch { process.exit(1); }
