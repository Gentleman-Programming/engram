import assert from "node:assert/strict";
import { mkdtemp, mkdir, rm, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { test } from "node:test";
import { child, localProvider, rpc, run } from "./support/native-host.mjs";

const root = fileURLToPath(new URL("../../../", import.meta.url));
const cli = process.env.ENGRAM_PI_LIFECYCLE_CLI;

test("actual Pi hosts retain attribution through compaction, resume and refusal", {
  skip: !cli && "opt-in: set ENGRAM_PI_LIFECYCLE_CLI to the installed Pi CLI entry point",
  timeout: 180_000,
}, async t => {
  const dir = await mkdtemp(join(root, ".pi-lifecycle-"));
  const processes = [];
  let provider, primaryFailure, refusalWarning;
  const evidence = { synthetic: "local model responses only", identities: [], commands: [], reads: [] };
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) =>
    /^(path|systemroot|windir|pathext|comspec|temp|tmp)$/i.test(key)));
  Object.assign(env, { HOME: dir, USERPROFILE: dir, APPDATA: dir, LOCALAPPDATA: dir,
    PI_OFFLINE: "1", PI_TELEMETRY: "0", PI_SKIP_VERSION_CHECK: "1",
    ENGRAM_PROJECT: "pi-lifecycle-1534", ENGRAM_CLOUD_AUTOSYNC: "0", ENGRAM_PI_SESSION_TRACE: "1" });
  try {
    const binary = join(dir, process.platform === "win32" ? "server.exe" : "server");
    evidence.build = await run("go", ["build", "-o", binary, "./plugin/pi/test/support/real-server"], {
      cwd: root, env: { ...process.env, GOPROXY: "off", GOTOOLCHAIN: "local" },
    });
    const server = child(binary, [join(dir, "store")], { cwd: dir, env });
    processes.push(server);
    const url = await server.wait(lines => lines.find(line => /^http:\/\/127\.0\.0\.1:\d+$/.test(line)));
    const get = async path => {
      const response = await fetch(url + path, { signal: AbortSignal.timeout(5000) });
      const data = await response.json();
      assert.equal(response.status, 200, JSON.stringify(data));
      evidence.reads.push({ path, status: response.status, data });
      return data;
    };
    evidence.health = await get("/health");
    evidence.revision = await run("git", ["rev-parse", "HEAD"], { cwd: root });
    evidence.node = process.version;
    evidence.go = await run("go", ["version"], { cwd: root });
    evidence.companion = "checkout plugin/pi at the recorded revision";
    evidence.pi = await run(process.execPath, [resolve(cli), "--version"], { cwd: dir, env });
    provider = await localProvider();
    const project = join(dir, "project");
    await mkdir(project);
    const start = async (label, sessionFile) => {
      const agentDir = join(dir, label);
      await mkdir(agentDir, { recursive: true });
      await writeFile(join(agentDir, "models.json"), JSON.stringify({ providers: { fixture: {
        baseUrl: provider.url + "/v1", api: "openai-completions", apiKey: "fixture-only",
        models: [{ id: "fixture", reasoning: false, input: ["text"], contextWindow: 32000, maxTokens: 2048,
          cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 } }],
      } } }));
      await writeFile(join(agentDir, "settings.json"), JSON.stringify({
        compaction: { enabled: false, keepRecentTokens: 128, reserveTokens: 1024 },
        retry: { enabled: false },
      }));
      const args = [resolve(cli), "--mode", "rpc", "--offline", "--no-approve", "--no-extensions",
        "--no-mcp", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files",
        "-e", join(root, "plugin/pi/index.ts"), "--provider", "fixture", "--model", "fixture",
        "--session-dir", join(dir, "sessions"), "--tools", "mem_save,mem_session_finalize"];
      if (sessionFile) args.push("--session", sessionFile);
      evidence.commands.push({ executable: process.execPath, args, cwd: project,
        env: { ...env, PI_CODING_AGENT_DIR: agentDir, ENGRAM_URL: url } });
      const host = rpc(process.execPath, args, { cwd: project,
        env: { ...env, PI_CODING_AGENT_DIR: agentDir, ENGRAM_URL: url } });
      processes.push(host);
      const state = await host.command("get_state");
      assert.ok(state.sessionId); assert.ok(state.sessionFile);
      evidence.identities.push({ label, ...state });
      return { host, state };
    };
    let a = await start("A"), b = await start("B");
    assert.notEqual(a.state.sessionId, b.state.sessionId);
    const expected = new Map();
    const observations = () => get("/observations?all_projects=true&limit=100");
    const verify = async () => {
      const rows = await observations();
      for (const [marker, owner] of expected) {
        const matches = rows.filter(row => row.title === marker);
        assert.equal(matches.length, 1, marker);
        assert.equal(matches[0].session_id, owner, marker);
      }
      return rows;
    };
    const save = async (actor, marker, owner = actor.state.sessionId) => {
      const result = await actor.host.tool("mem_save", {
        title: marker, content: `Isolated native-host marker ${marker}`, type: "discovery", capture_prompt: false,
      });
      assert.equal(result.isError, false, JSON.stringify(result));
      expected.set(marker, owner);
      const rows = await verify();
      const row = rows.find(row => row.title === marker);
      const session = await get(`/sessions/${encodeURIComponent(owner)}`);
      assert.ok(JSON.stringify(session).includes(owner));
      return row.session_id;
    };
    await save(a, "A-before"); await save(b, "B-before");
    for (const actor of [a, b]) {
      const result = await actor.host.command("compact");
      assert.ok(result.summary);
      const entries = (await actor.host.command("get_entries")).entries;
      assert.ok(entries.some(entry => entry.type === "compaction" && entry.summary === result.summary));
      assert.ok(actor.host.records.some(event => event.type === "compaction_end" && !event.aborted && event.result));
      const archived = (await observations()).filter(row => row.session_id === actor.state.sessionId
        && row.type === "session_summary" && row.content.includes(result.summary));
      assert.equal(archived.length, 1, "compaction must archive under its confirmed owner");
    }
    await save(a, "A-compacted"); await save(b, "B-compacted");
    for (const label of ["A", "B"]) {
      const previous = label === "A" ? a : b;
      await previous.host.stop();
      const resumed = await start(label, previous.state.sessionFile);
      assert.equal(resumed.state.sessionId, previous.state.sessionId);
      const marker = `${label}-resumed`;
      const result = await resumed.host.tool("mem_save", { title: marker, content: marker, capture_prompt: false });
      assert.equal(result.isError, false, JSON.stringify(result));
      const entries = (await resumed.host.command("get_entries")).entries;
      assert.ok(entries.some(entry => entry.type === "compaction"), "resume must restore real compacted history");
      const mapping = entries.filter(entry => entry.type === "custom" && entry.data?.runtimeID === resumed.state.sessionId
        && entry.data?.effectiveID).at(-1)?.data;
      assert.ok(mapping, "resume must retain the Go-confirmed continuation mapping");
      assert.ok(mapping.effectiveID.startsWith(`${resumed.state.sessionId}:resume:`));
      evidence.identities.push({ label, runtimeID: resumed.state.sessionId, effectiveID: mapping.effectiveID });
      expected.set(marker, mapping.effectiveID);
      await get(`/sessions/${encodeURIComponent(mapping.effectiveID)}`);
      if (label === "A") a = resumed; else b = resumed;
      await verify();
      const peer = label === "A" ? b : a;
      const peerOwner = expected.get(label === "A" ? "B-before" : "A-resumed");
      await save(peer, `${label}-resume-peer`, peerOwner);
    }
    const finalized = await a.host.tool("mem_session_finalize", { summary: "Goal: isolated lifecycle. Accomplished: verified markers. Next Steps: none." });
    assert.equal(finalized.isError, false, JSON.stringify(finalized));
    const before = await observations();
    const stats = await get("/stats");
    const refusalStart = a.host.records.length;
    const rejected = await a.host.tool("mem_save", { title: "A-rejected", content: "Must not persist", capture_prompt: false });
    assert.equal(rejected.isError, true, JSON.stringify(rejected));
    assert.match(JSON.stringify(rejected.content), /Pi runtime session is closing/);
    const refusalWarnings = a.host.records.slice(refusalStart).filter(record => record.notifyType === "warning");
    assert.equal(refusalWarnings.length, 1);
    refusalWarning = refusalWarnings[0];
    assert.equal(refusalWarning.message, "[engram] background capture to /sessions failed: Pi runtime session is closing");
    assert.deepEqual(await observations(), before);
    assert.deepEqual(await get("/stats"), stats);
    assert.ok(!(await verify()).some(row => row.title === "A-rejected"));
    await save(b, "B-after-refusal", expected.get("B-resumed"));
    evidence.providerRequests = provider.requests;
    assert.ok(provider.requests.some(request => request.compaction), "default compaction must reach the local provider");
  } catch (error) { primaryFailure = error; throw error; }
  finally {
    const cleanup = [];
    // Hosts must deliver shutdown to Go before its owning stdin closes.
    for (const process of [...processes].reverse()) {
      try { await process.stop(); } catch (error) { cleanup.push(String(error)); }
    }
    if (provider) {
      try { await provider.close(); } catch (error) { cleanup.push(String(error)); }
    }
    evidence.processes = processes.map(process => ({ exit: process.exit, stderr: process.stderr, records: process.records }));
    const warnings = evidence.processes.flatMap(process => process.records).filter(record =>
      record?.type === "extension_ui_request" && record.notifyType === "warning");
    evidence.cleanup = { errors: cleanup, warnings };
    t.diagnostic(JSON.stringify(evidence));
    try { await rm(dir, { recursive: true, force: true }); } catch (error) { cleanup.push(String(error)); }
    if (!primaryFailure) {
      assert.deepEqual(cleanup, [], "child cleanup failed");
      assert.deepEqual(warnings, [refusalWarning], "only the intentional ended-owner refusal may warn");
    }
  }
});
