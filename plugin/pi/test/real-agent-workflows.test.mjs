import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { once } from "node:events";
import { existsSync, readFileSync } from "node:fs";
import { randomBytes } from "node:crypto";
import { createInterface } from "node:readline";
import { join } from "node:path";
import { test } from "node:test";
import { PLUGIN_ROOT, importPluginFromSandbox, withPluginSandbox } from "./plugin-sandbox.mjs";

const project = "integration-1348";
const repositoryRoot = join(PLUGIN_ROOT, "../..");
const repositoryFixtureAvailable = [
  "go.mod", "internal/server/server.go", "internal/store/store.go",
  "plugin/pi/test/support/real-server/main.go",
].every(path => existsSync(join(repositoryRoot, path)));
const realServerSkip = !process.env.ENGRAM_PI_TEST_SERVER_BINARY && !repositoryFixtureAvailable
  ? "requires the Engram Go checkout or ENGRAM_PI_TEST_SERVER_BINARY" : false;

function realTest(name, run) {
  return test(name, { skip: realServerSkip }, run);
}

async function fixture(run) {
  await withPluginSandbox("engram-real-workflows-", async ({ dir, sandbox }) => {
    const keys = ["ENGRAM_URL", "ENGRAM_PROJECT", "ENGRAM_HTTP_TOKEN", "ENGRAM_DATA_DIR", "ENGRAM_CLOUD_AUTOSYNC", "HOME"];
    const previous = Object.fromEntries(keys.map(key => [key, process.env[key]]));
    const token = randomBytes(32).toString("hex");
    const executable = process.env.ENGRAM_PI_TEST_SERVER_BINARY ?? join(dir, process.platform === "win32" ? "server.exe" : "server");
    if (!process.env.ENGRAM_PI_TEST_SERVER_BINARY) {
      const build = spawnSync("go", ["build", "-o", executable, "./plugin/pi/test/support/real-server"],
        { cwd: repositoryRoot, env: process.env, encoding: "utf8", timeout: 60000 });
      assert.ifError(build.error);
      assert.equal(build.status, 0, build.stderr);
    }
    const env = { ...process.env, HOME: dir, ENGRAM_PROJECT: project, ENGRAM_HTTP_TOKEN: token,
      ENGRAM_DATA_DIR: join(dir, "data"), ENGRAM_CLOUD_AUTOSYNC: "0" };
    const child = spawn(executable, [join(dir, "store")], { env, stdio: ["pipe", "pipe", "pipe"] });
    let stderr = "";
    child.stderr.on("data", value => { stderr += value; });
    const lines = createInterface({ input: child.stdout });
    let bodySucceeded = false;
    try {
      const url = await new Promise((resolve, reject) => {
        const timer = setTimeout(() => finish(new Error(`server readiness timed out: ${stderr}`)), 5000);
        const onLine = value => finish(undefined, value);
        const onError = error => finish(error);
        const onExit = code => finish(new Error(`server exited ${code}: ${stderr}`));
        const finish = (error, value) => {
          clearTimeout(timer); lines.off("line", onLine); child.off("error", onError); child.off("exit", onExit);
          if (error) reject(error); else resolve(value);
        };
        lines.once("line", onLine); child.once("error", onError); child.once("exit", onExit);
      });
      Object.assign(process.env, env, { ENGRAM_URL: url });
      const tools = new Map(), events = new Map(), entries = [];
      const register = await importPluginFromSandbox(sandbox);
      register({ registerTool(tool) { tools.set(tool.name, tool); }, on(name, callback) { events.set(name, callback); },
        appendEntry(customType, data) { entries.push({ type: "custom", customType, data }); } });
      const ctx = { cwd: sandbox, sessionManager: { getSessionId: () => "real-host", getBranch: () => entries }, ui: { setStatus() {} } };
      const tool = async (name, params) => {
        assert.ok(tools.has(name), `${name} is registered`);
        return tools.get(name).execute("real-workflow", params, undefined, undefined, ctx);
      };
      const request = async (path, body, authorized = true) => {
        const response = await fetch(url + path, { method: body === undefined ? "GET" : "POST",
          headers: { "Content-Type": "application/json", ...(authorized ? { Authorization: `Bearer ${token}` } : {}) },
          body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(5000) });
        return { status: response.status, data: await response.json() };
      };
      const rows = async () => {
        const result = await request("/observations?all_projects=true&limit=100");
        assert.equal(result.status, 200); return result.data;
      };
      const originalFetch = globalThis.fetch, calls = [];
      // Observe real requests only; every response still comes from production Go handlers.
      globalThis.fetch = (input, init = {}) => {
        if (String(input).startsWith(url + "/")) calls.push({ path: new URL(input).pathname,
          method: init.method ?? "GET", authorized: new Headers(init.headers).get("authorization") === `Bearer ${token}` });
        return originalFetch(input, init);
      };
      try { await run({ tool, request, rows, events, ctx, child, calls }); }
      finally { globalThis.fetch = originalFetch; }
      bodySucceeded = true;
    } finally {
      try {
        for (const [key, value] of Object.entries(previous)) {
          if (value === undefined) delete process.env[key]; else process.env[key] = value;
        }
        lines.close();
        let shutdownTimedOut = false;
        if (child.exitCode === null && child.signalCode === null) {
          const exit = once(child, "exit");
          const timer = setTimeout(() => { shutdownTimedOut = true; child.kill(); }, 5000);
          try { child.stdin.end(); await exit; } finally { clearTimeout(timer); }
        }
        if (bodySucceeded) {
          assert.equal(shutdownTimedOut, false, `server shutdown timed out: ${stderr}`);
          assert.equal(child.exitCode, 0, stderr);
        }
      } catch (cleanupError) {
        // Cleanup must not replace an existing readiness or callback failure.
        if (bodySucceeded) throw cleanupError;
      }
    }
  });
}

realTest("real fixture preserves the original callback failure after child termination", async () => {
  const original = new Error("original workflow assertion");
  await assert.rejects(fixture(async ({ child }) => {
    const exit = once(child, "exit");
    child.kill();
    await exit;
    throw original;
  }), error => error === original);
});

realTest("real fixture accepts a successful callback and clean shutdown", async () => {
  await fixture(async () => {});
});

realTest("real fixture reports shutdown timeout after a successful callback", async () => {
  await assert.rejects(fixture(async ({ child }) => {
    // Keep stdin open so the existing shutdown deadline must terminate the child.
    child.stdin.end = () => child.stdin;
  }), /server shutdown timed out/);
});

async function save(tool, title, content) {
  const result = await tool("mem_save", { title, content, project });
  assert.notEqual(result.isError, true, JSON.stringify(result));
  assert.ok(Number.isSafeInteger(result.details.data.id));
  return result.details.data.id;
}

realTest("real runtime metadata binds native requests to the generated authenticated Go contract", async () => {
  await fixture(async ({ tool, request, rows, calls }) => {
    const metadata = await request("/runtime-contract", undefined, false);
    assert.equal(metadata.status, 200, "shared read-only metadata follows health's public policy");
    const { instance_id: instanceID, ...projection } = metadata.data;
    assert.deepEqual(projection, JSON.parse(readFileSync(new URL("../runtime-contract.json", import.meta.url), "utf8")));
    assert.equal(typeof instanceID, "string");
    assert.ok(instanceID.length > 0);
    assert.equal((await request("/health")).data.instance_id, instanceID);
    const before = calls.length;
    const result = await tool("mem_suggest_topic_key", { type: "architecture", title: "Runtime qualification" });
    assert.notEqual(result.isError, true, JSON.stringify(result));
    const native = calls.slice(before);
    assert.ok(native.every(call => call.authorized), "native qualification and operation retain the shared credential");
    const qualified = native.findIndex(call => call.path === "/runtime-contract" && call.method === "GET");
    assert.ok(qualified >= 0);
    assert.ok(native.findIndex(call => call.path === "/topic-keys/suggest" && call.method === "POST") > qualified);
    assert.equal((await request("/observations/consolidate", {}, false)).status, 401);
    assert.deepEqual(await rows(), []);
    assert.equal((await request("/sessions/real-host")).status, 404);
  });
});

realTest("real Pi topic vectors match Go without registering sessions or creating observations", async () => {
  await fixture(async ({ tool, request, rows }) => {
    for (const params of [{ type: "architecture", title: "Auth model" }, { type: "bugfix", title: "Fix café / Unicode" },
      { type: "decision", content: "Use SQLite for local state" }, { title: "X".repeat(120) }]) {
      const expected = await request("/topic-keys/suggest", params);
      assert.equal(expected.status, 200);
      const actual = await tool("mem_suggest_topic_key", params);
      assert.notEqual(actual.isError, true, JSON.stringify(actual));
      assert.deepEqual(actual.details.data, expected.data);
      if (params.title === "Auth model") assert.equal(expected.data.topic_key, "architecture/auth-model");
    }
    const invalid = await tool("mem_suggest_topic_key", {});
    assert.equal(invalid.isError, true); assert.equal(invalid.details.http_status, 400);
    assert.equal(invalid.content[0].text, "provide title or content to suggest a topic_key");
    assert.deepEqual(await rows(), []);
    assert.equal((await request("/sessions/real-host")).status, 404);
  });
});

realTest("real Pi consolidation previews without mutation and confirms the exact Go plan", async () => {
  await fixture(async ({ tool, request, rows }) => {
    const ids = [await save(tool, "First design", "First independent source record."),
      await save(tool, "Second design", "Second independent source record.")];
    const params = { ids_to_delete: ids, new_title: "Combined design", new_content: "Consolidated public summary", expected_project: project };
    const before = await rows();
    const unauthorized = await request("/observations/consolidate", params, false);
    assert.equal(unauthorized.status, 401); assert.deepEqual(await rows(), before);
    const preview = await tool("mem_consolidate", params);
    assert.notEqual(preview.isError, true, JSON.stringify(preview));
    assert.equal(preview.details.data.status, "preview");
    assert.ok(preview.details.data.plan.fingerprint);
    assert.deepEqual(await rows(), before);
    const confirmed = await tool("mem_consolidate", { ...params, confirm: true, fingerprint: preview.details.data.plan.fingerprint });
    assert.notEqual(confirmed.isError, true, JSON.stringify(confirmed));
    assert.equal(confirmed.details.data.status, "consolidated");
    const after = await rows(); assert.equal(after.length, 1);
    assert.equal(after[0].title, params.new_title); assert.match(after[0].content, /Consolidated public summary/);
    assert.deepEqual([...confirmed.details.data.soft_deleted_ids].sort(), [...ids].sort());
  });
});

realTest("real Pi batch envelopes and ordered item errors leave public state unchanged", async () => {
  await fixture(async ({ tool, request, rows }) => {
    await save(tool, "Batch source", "Existing observation must remain unchanged.");
    const before = await rows();
    const conflicts = await request(`/conflicts?project=${project}`); assert.equal(conflicts.status, 200);
    const invalid = await tool("mem_judge_many", { judgments: [] });
    assert.equal(invalid.isError, true); assert.equal(invalid.details.http_status, 400);
    const judgments = [{ judgment_id: "missing-first", relation: "compatible", confidence: 0 },
      { judgment_id: "missing-second", relation: "related" }];
    const unauthorized = await request("/conflicts/judge-many", { judgments }, false);
    assert.equal(unauthorized.status, 401);
    const result = await tool("mem_judge_many", { judgments });
    assert.notEqual(result.isError, true, JSON.stringify(result));
    const outcomes = result.details.data.outcomes;
    assert.equal(outcomes.length, 2);
    assert.deepEqual(outcomes.map(value => [value.index, value.judgment_id, value.status]),
      [[0, "missing-first", "error"], [1, "missing-second", "error"]]);
    assert.ok(outcomes.every(value => typeof value.error === "string" && value.error.length));
    assert.deepEqual(await rows(), before);
    assert.deepEqual(await request(`/conflicts?project=${project}`), conflicts);
    // No public pending-relation seed is established here; this does not prove a successful batch judgment write.
  });
});

realTest("real Pi judgments persist successful and mixed outcomes from public scan seeds", async () => {
  await fixture(async ({ tool, request, rows }) => {
    for (const title of ["JWT authentication token session management policy",
      "Session token JWT authentication management approach",
      "Authentication JWT session token policy decision"]) {
      await save(tool, title, `JWT auth content for ${title}`);
    }
    const observations = await rows();
    const scan = await request("/conflicts/scan", { project, apply: true });
    assert.equal(scan.status, 200);
    assert.ok(scan.data.inserted >= 2, JSON.stringify(scan));
    assert.equal(scan.data.dry_run, false);
    for (const key of ["semantic_judged", "semantic_skipped", "semantic_errors"]) assert.equal(scan.data[key], 0);
    const listed = await request(`/conflicts?project=${project}&status=pending`);
    assert.equal(listed.status, 200);
    assert.equal(listed.data.total, scan.data.inserted);
    const [first, second] = listed.data.relations;
    // Judge extra candidates publicly so exactly two remain pending.
    for (const extra of listed.data.relations.slice(2)) {
      const seeded = await tool("mem_judge_many", { judgments: [
        { judgment_id: extra.sync_id, relation: "related", confidence: 0.5 },
      ] });
      assert.notEqual(seeded.isError, true, JSON.stringify(seeded));
      assert.equal(seeded.details.data.outcomes[0].status, "success");
    }
    const stats = async () => {
      const result = await request(`/conflicts/stats?project=${project}`);
      assert.equal(result.status, 200); return result.data;
    };
    const beforeStats = await stats();
    assert.equal(beforeStats.by_judgment_status.pending, 2);
    const success = await tool("mem_judge_many", { judgments: [
      { judgment_id: first.sync_id, relation: "compatible", confidence: 0 },
    ] });
    assert.notEqual(success.isError, true, JSON.stringify(success));
    assert.deepEqual(success.details.data.outcomes.map(({ index, judgment_id, status, judgment_status }) =>
      ({ index, judgment_id, status, judgment_status })),
    [{ index: 0, judgment_id: first.sync_id, status: "success", judgment_status: "judged" }]);
    const persisted = await request(`/conflicts/${first.id}`);
    assert.equal(persisted.status, 200);
    assert.equal(persisted.data.relation, "compatible");
    assert.equal(persisted.data.judgment_status, "judged");
    assert.equal(success.details.data.outcomes[0].relation.confidence, 0);
    const invalidVerb = 'JudgeRelation: invalid relation verb "invalid" — must be one of: related, compatible, scoped, conflicts_with, supersedes, not_conflict';
    const mixed = await tool("mem_judge_many", { judgments: [
      { judgment_id: "missing-before", relation: "related" },
      { judgment_id: second.sync_id, relation: "conflicts_with", confidence: 1 },
      { judgment_id: second.sync_id, relation: "invalid" },
      { judgment_id: second.sync_id, relation: "related", confidence: -0.01 },
    ] });
    assert.notEqual(mixed.isError, true, JSON.stringify(mixed));
    const outcomes = mixed.details.data.outcomes;
    assert.deepEqual(outcomes.map(({ index, judgment_id, status }) => ({ index, judgment_id, status })), [
      { index: 0, judgment_id: "missing-before", status: "error" },
      { index: 1, judgment_id: second.sync_id, status: "success" },
      { index: 2, judgment_id: second.sync_id, status: "error" },
      { index: 3, judgment_id: second.sync_id, status: "error" },
    ]);
    assert.equal(outcomes[0].error, 'JudgeRelation: relation "missing-before" not found');
    assert.equal(outcomes[2].error, invalidVerb);
    assert.equal(outcomes[3].error, "confidence must be a number between 0.0 and 1.0");
    assert.equal(outcomes[1].relation.confidence, 1);
    const secondRead = await request(`/conflicts/${second.id}`);
    assert.equal(secondRead.status, 200);
    assert.equal(secondRead.data.relation, "conflicts_with");
    assert.equal(secondRead.data.judgment_status, "judged");
    const afterStats = await stats();
    assert.equal(Object.hasOwn(afterStats.by_judgment_status, "pending"), false);
    assert.equal(afterStats.by_judgment_status.pending ?? 0, beforeStats.by_judgment_status.pending - 2);
    assert.equal(afterStats.by_judgment_status.judged, (beforeStats.by_judgment_status.judged ?? 0) + 2);
    assert.equal(afterStats.by_relation.compatible, (beforeStats.by_relation.compatible ?? 0) + 1);
    assert.equal(afterStats.by_relation.conflicts_with, (beforeStats.by_relation.conflicts_with ?? 0) + 1);
    // A rejected envelope never undoes the earlier independently committed item.
    const invalid = await tool("mem_judge_many", { judgments: [] });
    assert.equal(invalid.isError, true);
    assert.equal(invalid.details.http_status, 400);
    assert.equal(invalid.content[0].text, "judgments must be an array containing 1..32 items");
    assert.deepEqual(await request(`/conflicts/${second.id}`), secondRead);
    assert.deepEqual(await stats(), afterStats);
    assert.deepEqual(await rows(), observations);
  });
});

realTest("real Pi finalization returns Go's original summary receipt and leaves the session ended", async () => {
  await fixture(async ({ tool, request, rows, events, ctx }) => {
    const unregistered = await tool("mem_session_finalize", { summary: "No session yet" });
    assert.equal(unregistered.isError, true); assert.deepEqual(await rows(), []);
    assert.equal((await request("/sessions/real-host")).status, 404);
    await save(tool, "Before finalization", "Owned source record.");
    const unauthorized = await request("/sessions/real-host/finalize", { project, summary: "Unauthorized" }, false);
    assert.equal(unauthorized.status, 401);
    const before = await rows();
    const finalized = await tool("mem_session_finalize", { summary: "Goal: completed real HTTP proof" });
    assert.notEqual(finalized.isError, true, JSON.stringify(finalized));
    assert.equal(finalized.details.data.state, "finalized"); assert.equal(finalized.details.data.session_id, "real-host");
    const retry = await tool("mem_session_finalize", { summary: "Different retry text" });
    assert.notEqual(retry.isError, true, JSON.stringify(retry));
    assert.equal(retry.details.data.state, "already_finalized");
    assert.equal(retry.details.data.summary_id, finalized.details.data.summary_id);
    assert.equal(retry.details.data.summary_sync_id, finalized.details.data.summary_sync_id);
    const session = await request("/sessions/real-host"); assert.equal(session.status, 200); assert.ok(session.data.ended_at);
    const after = await rows(); assert.equal(after.length, before.length + 1);
    const summary = after.find(value => value.id === finalized.details.data.summary_id);
    assert.equal(summary.type, "session_summary"); assert.equal(summary.session_id, "real-host");
    assert.equal(summary.content, "Goal: completed real HTTP proof");
    assert.equal((await tool("mem_save", { title: "Late", content: "Must not save" })).isError, true);
    await events.get("session_shutdown")({}, ctx);
    assert.deepEqual(await rows(), after);
    assert.deepEqual(await request("/sessions/real-host"), session);
  });
});
