import { spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { isIP } from "node:net";
import { fileURLToPath } from "node:url";

const result = { name: "production-core-lifecycle", state: "unavailable", cleanup: "not-started", joined: true, nodeVersion: process.versions.node };
const token = randomBytes(32).toString("hex");
const workflow = process.argv[4] || "";
let core, exited, pi, piExited, parentGone = false;
let coreStopRequested = false, coreEndedEarly = false;
if (process.argv[3]) result.piBootstrap = { name: workflow ? `pi-workflow-${workflow}` : "pi-module-bootstrap-only", state: "unavailable", cleanup: "not-started", joined: true };
process.stdin.on("end", () => {
  parentGone = true;
  if (pi) pi.stdin.end(); // Core remains available through ordered Pi cleanup.
  else { coreStopRequested = true; core?.stdin.end(); }
});
process.stdin.resume();
async function bounded(promise, milliseconds) {
  let timer;
  try { return await Promise.race([promise, new Promise((_, reject) => { timer = setTimeout(() => reject(new Error("deadline")), milliseconds); })]); }
  finally { clearTimeout(timer); }
}
// A post-spawn error is not an exit. Only a failed creation with no PID is joined.
function termination(child) {
  return new Promise(resolve => {
    child.on("error", () => { if (!child.pid) resolve({ code: null, failedStart: true }); });
    child.once("exit", (code, signal) => resolve({ code, signal }));
  });
}
async function join(child, exited, observation) {
  child.stdin.end();
  try {
    const exit = await bounded(exited, 3000);
    observation.joined = true;
    observation.exitCode = exit.code;
    observation.cleanup = exit.failedStart ? "not-started" : exit.code === 0 ? "passed" : "failed";
  } catch {
    child.kill();
    try {
      const exit = await bounded(exited, 2000);
      observation.joined = true;
      observation.exitCode = exit.code;
      observation.cleanup = "killed";
    } catch { observation.cleanup = "unknown"; }
  }
}
try {
  if ((workflow && !["topics", "consolidation", "batch-errors", "judgments"].includes(workflow)) || Number(process.versions.node.split(".")[0]) < 24 || parentGone) throw new Error("prerequisite");
  const keys = ["HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "TMP", "TEMP", "TMPDIR", "ENGRAM_DATA_DIR"];
  const env = Object.fromEntries(keys.map(key => [key, process.env[key]]));
  env.ENGRAM_TOKEN = token; // Never serialize the token or forward child diagnostics.
  core = spawn(process.argv[2], ["serve", "--session-owned"], { cwd: process.cwd(), env, stdio: ["pipe", "pipe", "ignore"] });
  core.stdin.on("error", () => {});
  result.joined = false;
  result.cleanup = "unknown";
  exited = termination(core);
  core.once("exit", () => { if (!coreStopRequested) coreEndedEarly = true; });
  const readiness = new Promise((resolve, reject) => {
    let data = "";
    core.once("error", reject);
    core.once("exit", () => reject(new Error("early exit")));
    core.stdout.on("data", chunk => {
      data += chunk;
      if (Buffer.byteLength(data) > 4096) return reject(new Error("readiness limit"));
      if (data.includes("\n")) {
        try { resolve(JSON.parse(data.slice(0, data.indexOf("\n")))); } catch { reject(new Error("readiness")); }
      }
    });
  });
  const ready = await bounded(readiness, 5000);
  core.stdout.removeAllListeners("data");
  core.stdout.resume();
  const endpoint = new URL(ready.endpoint);
  const host = endpoint.hostname.replace(/^\[|\]$/g, "");
  if (ready.protocol !== "engram-session-owned-v1" || ready.pid !== core.pid || typeof ready.instance_id !== "string" || !ready.instance_id || ready.instance_id.length > 256 || endpoint.protocol !== "http:" || !isIP(host) || !(host.startsWith("127.") || host === "::1") || !Number(endpoint.port) || endpoint.username || endpoint.password || endpoint.search || endpoint.hash || endpoint.pathname !== "/") throw new Error("identity");
  const healthURL = new URL("/health", endpoint);
  const unauthorized = await fetch(healthURL, { signal: AbortSignal.timeout(2000), redirect: "error" });
  await unauthorized.body?.cancel();
  if (unauthorized.status !== 401) throw new Error("authentication");
  const response = await fetch(healthURL, { headers: { Authorization: `Bearer ${token}` }, signal: AbortSignal.timeout(2000), redirect: "error" });
  let body = "";
  for await (const chunk of response.body) {
    body += Buffer.from(chunk).toString();
    if (Buffer.byteLength(body) > 4096) throw new Error("health limit");
  }
  const health = JSON.parse(body);
  if (response.status !== 200 || health.status !== "ok" || health.service !== "engram" || health.instance_id !== ready.instance_id || typeof health.version !== "string" || !health.version || health.version.length > 256 || parentGone) throw new Error("health identity");
  result.instanceId = health.instance_id;
  result.reportedVersion = health.version; // Observed response, not manifest intent.
  result.state = "passed";
  if (result.piBootstrap) {
    const observation = result.piBootstrap;
    const piEnv = { ...env, ENGRAM_URL: endpoint.href, ENGRAM_HTTP_TOKEN: token,
      ENGRAM_EXPECTED_INSTANCE: ready.instance_id, ENGRAM_STARTUP_POLICY: workflow ? "allow" : "deny" };
    pi = spawn(process.execPath, ["--disable-warning=ExperimentalWarning", fileURLToPath(new URL("./pi-owner.mjs", import.meta.url)), process.argv[3], workflow], {
      cwd: process.cwd(), env: piEnv, stdio: ["pipe", "ignore", "ignore", "ipc"],
    });
    pi.stdin.on("error", () => {});
    observation.joined = false;
    observation.cleanup = "unknown";
    piExited = termination(pi);
    try {
      await bounded(new Promise((resolve, reject) => {
        pi.once("error", reject);
        pi.once("exit", () => reject(new Error("early exit")));
        pi.once("message", message => {
          if (message?.protocol !== (workflow ? `engram-pi-${workflow}-v1` : "engram-pi-bootstrap-v1")) return reject(new Error("protocol"));
          observation.substitutions = ["@earendil-works/pi-tui.Text", "Pi registration host"].filter(value => message.substitutions?.includes(value));
          resolve();
        });
      }), workflow ? 30000 : 2000);
      if (!parentGone && core.exitCode === null && core.signalCode === null) observation.state = "passed";
    } catch { observation.state = "unavailable"; }
  }
} catch { result.state = "unavailable"; }
finally {
  // Keep the same authenticated Core alive until its Pi child is joined.
  if (pi) {
    await join(pi, piExited, result.piBootstrap);
    if (parentGone || result.piBootstrap.cleanup !== "passed") result.piBootstrap.state = "unavailable";
  }
  if (core) {
    if (core.exitCode !== null || core.signalCode !== null) coreEndedEarly = true;
    // Unknown Pi termination still requires bounded best-effort Core cleanup.
    coreStopRequested = true;
    await join(core, exited, result);
  }
  if (result.piBootstrap && (coreEndedEarly || !result.piBootstrap.joined || result.cleanup !== "passed")) {
    result.piBootstrap.state = "unavailable";
  }
  if (result.piBootstrap && !result.piBootstrap.joined) result.joined = false;
  if (parentGone || coreEndedEarly || result.cleanup !== "passed") result.state = "unavailable";
  process.stdout.write(`${JSON.stringify(result)}\n`, () => process.exit(0));
}
