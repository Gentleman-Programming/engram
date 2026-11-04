import { spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { isIP } from "node:net";

const result = { name: "production-core-lifecycle", state: "unavailable", cleanup: "not-started", joined: true, nodeVersion: process.versions.node };
let token = randomBytes(32).toString("hex");
let core, exited, parentGone = false;
process.stdin.on("end", () => { parentGone = true; core?.stdin.end(); });
process.stdin.resume();
async function bounded(promise, milliseconds) {
  let timer;
  try { return await Promise.race([promise, new Promise((_, reject) => { timer = setTimeout(() => reject(new Error("deadline")), milliseconds); })]); }
  finally { clearTimeout(timer); }
}
async function startCore() {
  if (Number(process.versions.node.split(".")[0]) < 24 || parentGone) throw new Error("prerequisite");
  const keys = ["HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "TMP", "TEMP", "TMPDIR", "ENGRAM_DATA_DIR"];
  const env = Object.fromEntries(keys.map(key => [key, process.env[key]]));
  env.ENGRAM_TOKEN = token; // Never serialize the token or forward child diagnostics.
  core = spawn(process.argv[2], ["serve", "--session-owned"], { cwd: process.cwd(), env, stdio: ["pipe", "pipe", "ignore"] });
  core.stdin.on("error", () => {});
  result.joined = false;
  result.cleanup = "unknown";
  exited = new Promise(resolve => {
    core.on("error", () => {
      if (!core.pid) resolve({ code: null, failedStart: true });
    });
    core.once(process.argv[3] ? "close" : "exit", (code, signal) => resolve({ code, signal }));
  });
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
  endpointForRequest = endpoint;
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
  return health;
}
async function stopCore() {
  if (core) {
    if (!core.stdin.writableEnded) core.stdin.end();
    try {
      const exit = await bounded(exited, 3000);
      result.joined = true;
      result.exitCode = exit.code;
      result.cleanup = exit.failedStart ? "not-started" : exit.code === 0 ? "passed" : "failed";
    } catch {
      core.kill(); // Only this owned child, never a discovered PID/listener.
      try {
        const exit = await bounded(exited, 2000);
        result.joined = true;
        result.exitCode = exit.code;
        result.cleanup = "killed";
      } catch { result.cleanup = "unknown"; }
    }
  }
}
async function request(path, body) {
  const response = await fetch(new URL(path, endpointForRequest), {
    method: body === undefined ? "GET" : "POST", redirect: "error",
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(2000),
  });
  let text = "";
  for await (const chunk of response.body) {
    text += Buffer.from(chunk).toString();
    if (Buffer.byteLength(text) > 65536) throw new Error("response limit");
  }
  return { status: response.status, data: JSON.parse(text) };
}
let endpointForRequest;
try {
  const first = await startCore();
  if (process.argv[3]) {
    result.persistence = "unavailable";
    if (first.version !== process.argv[3]) throw new Error("version");
    const record = { session_id: `smoke-${randomBytes(12).toString("hex")}`, type: "decision",
      title: "Published artifact persistence", content: randomBytes(32).toString("hex"),
      project: "published-artifact-smoke", scope: "project" };
    if ((await request("/sessions", { id: record.session_id, project: record.project })).status !== 201) throw new Error("session");
    const saved = await request("/observations", record);
    if (saved.status !== 201 || !Number.isSafeInteger(saved.data.id) || saved.data.id < 1) throw new Error("save");
    const verify = async () => {
      const read = await request(`/observations/${saved.data.id}`);
      if (read.status !== 200 || !Object.entries(record).every(([k,v]) => read.data[k] === v)) throw new Error("readback");
    };
    await verify();
    await stopCore();
    if (!result.joined || result.cleanup !== "passed" || parentGone) throw new Error("first shutdown");
    token = randomBytes(32).toString("hex");
    const second = await startCore();
    if (second.instance_id !== first.instance_id || second.version !== process.argv[3]) throw new Error("reopen identity");
    await verify();
    result.persistence = "passed";
  }
  result.state = "passed";
} catch { result.state = "unavailable"; }
finally {
  await stopCore();
  if (parentGone || result.cleanup !== "passed") result.state = "unavailable";
  if (process.argv[3]) {
    process.stdin.pause();
    process.stdin.destroy();
    process.stdout.write(`${JSON.stringify(result)}\n`);
  } else process.stdout.write(`${JSON.stringify(result)}\n`, () => process.exit(0));
}
