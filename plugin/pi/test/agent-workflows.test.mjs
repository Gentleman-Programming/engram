import assert from "node:assert/strict";
import { test } from "node:test";
import { importPluginFromSandbox, withPluginSandbox } from "./plugin-sandbox.mjs";

async function fixture(body, handler) {
  const previousFetch = globalThis.fetch;
  const previousURL = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const calls = [];
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname;
    const call = { path, method: init.method ?? "GET", body: init.body && JSON.parse(init.body) };
    calls.push(call);
    if (path === "/health") return Response.json({ status: "ok" });
    if (path === "/project/current") return Response.json({ project: "engram" });
    if (handler) {
      const response = await handler(call, calls);
      if (response) return response;
    }
    if (path === "/sessions") return Response.json({ id: call.body.id, status: "created" }, { status: 201 });
    if (path === "/observations") return Response.json({ id: 4, status: "saved" }, { status: 201 });
    if (path.endsWith("/end")) return Response.json({ status: "ended" });
    throw new Error(`Unexpected request: ${path}`);
  };
  try {
    await withPluginSandbox("engram-agent-workflows-", async ({ sandbox }) => {
      const tools = new Map();
      const events = new Map();
      const entries = [];
      const register = await importPluginFromSandbox(sandbox);
      register({
        registerTool(tool) { tools.set(tool.name, tool); },
        on(name, callback) { events.set(name, callback); },
        appendEntry(customType, data) { entries.push({ type: "custom", customType, data }); },
      });
      const ctx = { cwd: sandbox, sessionManager: {
        getSessionId: () => "host-agent-workflows", getBranch: () => entries,
      }, ui: { setStatus() {} } };
      const run = (name, params, signal) => {
        assert.ok(tools.has(name), `${name} is registered`);
        return tools.get(name).execute("workflow", params, signal, undefined, ctx);
      };
      await body({ run, tools, events, ctx, calls });
    });
  } finally {
    globalThis.fetch = previousFetch;
    if (previousURL === undefined) delete process.env.ENGRAM_URL; else process.env.ENGRAM_URL = previousURL;
  }
}

const consolidate = { ids_to_delete: [1, 2], new_title: "Consolidated", new_content: "Public summary", expected_project: "engram" };
const finalReceipt = { session_id: "host-agent-workflows", state: "finalized", summary_id: 8,
  summary_sync_id: "summary-sync", already_finalized: false, judgment_required: false };
const mutations = (calls) => calls.filter(({ method }) => method !== "GET");

test("Pi consolidation forwards preview and explicit opaque confirmation without local planning", async () => {
  const plan = { fingerprint: "opaque-Go-plan", source_ids: [1, 2], project: "engram" };
  await fixture(async ({ run, tools, calls }) => {
    const schema = tools.get("mem_consolidate").parameters.args[0];
    assert.equal(schema.ids_to_delete.kind, "Array");
    assert.equal(schema.ids_to_delete.args[0].kind, "Integer");
    assert.equal(schema.ids_to_delete.args[0].args[0].maximum, Number.MAX_SAFE_INTEGER);
    const preview = await run("mem_consolidate", consolidate);
    assert.notEqual(preview.isError, true);
    assert.deepEqual(preview.details.data, { status: "preview", plan });
    const params = { ...consolidate, confirm: true, fingerprint: plan.fingerprint, type: "architecture", topic_key: "architecture/summary" };
    const confirmed = await run("mem_consolidate", params);
    assert.notEqual(confirmed.isError, true);
    assert.deepEqual(confirmed.details.data.soft_deleted_ids, [1, 2]);
    assert.deepEqual(mutations(calls), [
      { path: "/observations/consolidate", method: "POST", body: consolidate },
      { path: "/observations/consolidate", method: "POST", body: params },
    ]);
  }, ({ path, body }) => path === "/observations/consolidate"
    ? Response.json(body.confirm ? { status: "consolidated", plan, survivor: { id: 9 }, soft_deleted_ids: [1, 2] }
      : { status: "preview", plan }, { status: body.confirm ? 201 : 200 }) : undefined);
});

test("Pi consolidation preserves Go drift refusal and does not confirm its replacement plan", async () => {
  const refusal = { error: "preview changed", code: "fingerprint_mismatch", plan: { fingerprint: "replacement" } };
  await fixture(async ({ run, calls }) => {
    const result = await run("mem_consolidate", { ...consolidate, confirm: true, fingerprint: "original" });
    assert.equal(result.isError, true);
    assert.equal(result.details.http_status, 409);
    assert.deepEqual(result.details.data, refusal);
    assert.equal(result.content[0].text, refusal.error);
    assert.equal(mutations(calls).length, 1);
  }, ({ path }) => path === "/observations/consolidate" ? Response.json(refusal, { status: 409 }) : undefined);
});

test("Pi batch judgment uses one POST and preserves ordered partial outcomes and zero confidence", async () => {
  const judgments = [{ judgment_id: "first", relation: "compatible", confidence: 0 },
    { judgment_id: "missing", relation: "related", reason: "unknown" }];
  const outcomes = [{ index: 0, judgment_id: "first", status: "success", judgment_status: "judged" },
    { index: 1, judgment_id: "missing", status: "error", error: "unknown judgment" }];
  await fixture(async ({ run, calls }) => {
    const result = await run("mem_judge_many", { judgments, session_id: "shared-provenance" });
    assert.notEqual(result.isError, true);
    assert.deepEqual(result.details.data, { outcomes });
    assert.deepEqual(mutations(calls), [{ path: "/conflicts/judge-many", method: "POST", body: { judgments, session_id: "shared-provenance" } }]);
  }, ({ path }) => path === "/conflicts/judge-many" ? Response.json({ outcomes }) : undefined);
});

test("Pi batch judgment preserves Go batch rejection without individual fallback writes", async () => {
  await fixture(async ({ run, calls }) => {
    const result = await run("mem_judge_many", { judgments: [] });
    assert.equal(result.isError, true);
    assert.equal(result.details.http_status, 400);
    assert.equal(result.content[0].text, "judgments must contain 1..32 items");
    assert.equal(mutations(calls).length, 1);
  }, ({ path }) => path === "/conflicts/judge-many"
    ? Response.json({ error: "judgments must contain 1..32 items" }, { status: 400 }) : undefined);
});

test("Pi finalization requires confirmed ownership without implicitly creating a session", async () => {
  await fixture(async ({ run, tools, calls }) => {
    assert.equal(Object.hasOwn(tools.get("mem_session_finalize").parameters.args[0], "session_id"), false);
    const result = await run("mem_session_finalize", { summary: "Goal: completed" });
    assert.equal(result.isError, true);
    assert.match(result.content[0].text, /confirmed.*(?:registration|ownership)/);
    assert.deepEqual(mutations(calls), []);
  });
});

test("Pi finalization pins host identity and original Go summary across retries and shutdown", async () => {
  await fixture(async ({ run, events, ctx, calls }) => {
    assert.notEqual((await run("mem_save", { title: "Before", content: "existing record" })).isError, true);
    const result = await run("mem_session_finalize", { summary: "Goal: completed" });
    assert.notEqual(result.isError, true);
    assert.deepEqual(result.details.data, finalReceipt);
    const retry = await run("mem_session_finalize", { summary: "Different retry text" });
    assert.deepEqual(retry.details.data, { ...finalReceipt, state: "already_finalized", already_finalized: true });
    const later = await run("mem_save", { title: "Late", content: "must not save" });
    assert.equal(later.isError, true);
    assert.match(later.content[0].text, /closing/);
    await events.get("session_shutdown")({}, ctx);
    assert.deepEqual(mutations(calls).map(({ path }) => path), ["/sessions", "/observations",
      "/sessions/host-agent-workflows/finalize", "/sessions/host-agent-workflows/finalize"]);
    assert.deepEqual(calls.filter(({ path }) => path.endsWith("/finalize")).map(({ body }) => body), [
      { summary: "Goal: completed", project: "engram" }, { summary: "Different retry text", project: "engram" },
    ]);
  }, ({ path }, calls) => path.endsWith("/finalize") ? Response.json(calls.filter(({ path }) => path.endsWith("/finalize")).length === 1
    ? finalReceipt : { ...finalReceipt, state: "already_finalized", already_finalized: true }) : undefined);
});

for (const params of [{ summary: "Goal", session_id: "foreign" }, { summary: "Goal", project: "foreign" }]) {
  test(`Pi finalization rejects ${Object.hasOwn(params, "session_id") ? "model identity override" : "foreign owner"} before writes`, async () => {
    await fixture(async ({ run, calls }) => {
      await run("mem_save", { title: "Before", content: "owned" });
      const before = mutations(calls).length;
      const result = await run("mem_session_finalize", params);
      assert.equal(result.isError, true);
      assert.equal(mutations(calls).length, before);
      assert.match(result.content[0].text, /(?:runtime|host|owner|project)/i);
    });
  });
}

for (const name of ["mem_consolidate", "mem_judge_many", "mem_session_finalize"]) {
  test(`Pi ${name} cancellation before dispatch sends no mutation`, async () => {
    await fixture(async ({ run, calls }) => {
      const controller = new AbortController();
      controller.abort();
      await assert.rejects(run(name, name === "mem_consolidate" ? consolidate : name === "mem_judge_many" ? { judgments: [] } : { summary: "Goal" }, controller.signal), /abort/i);
      assert.deepEqual(mutations(calls), []);
    });
  });
}

for (const name of ["mem_consolidate", "mem_judge_many"]) {
  test(`Pi ${name} ambiguous POST is not replayed`, async () => {
    await fixture(async ({ run, calls }) => {
      const result = await run(name, name === "mem_consolidate" ? consolidate : { judgments: [{ judgment_id: "one", relation: "related" }] });
      assert.equal(result.isError, true);
      assert.equal(result.details.outcome, "unknown");
      assert.equal(mutations(calls).length, 1);
    }, ({ path }) => {
      if (path === "/observations/consolidate" || path === "/conflicts/judge-many") throw new TypeError("fetch failed");
    });
  });
}

test("Pi legacy summary and current-host end retain their existing routes", async () => {
  await fixture(async ({ run, calls }) => {
    assert.notEqual((await run("mem_session_summary", { content: "Legacy summary" })).isError, true);
    assert.notEqual((await run("mem_session_end", { id: "host-agent-workflows", summary: "Free text" })).isError, true);
    assert.deepEqual(mutations(calls).map(({ path }) => path), ["/sessions", "/observations", "/sessions/host-agent-workflows/end"]);
    assert.equal(calls.find(({ path }) => path === "/observations").body.type, "session_summary");
  });
});

for (const lifecycleEvent of ["session_shutdown", "session_start"]) {
  test(`Pi unknown finalization remains pinned through ${lifecycleEvent}, without legacy end or reopen`, async () => {
    await fixture(async ({ run, events, ctx, calls }) => {
      await run("mem_save", { title: "Before", content: "owned" });
      const failed = await run("mem_session_finalize", { summary: "Goal: completed" });
      assert.equal(failed.isError, true);
      assert.equal(failed.details.outcome, "unknown");
      await events.get(lifecycleEvent)({}, ctx);
      const late = await run("mem_save", { title: "Late", content: "must not save" });
      assert.equal(late.isError, true);
      const retry = await run("mem_session_finalize", { summary: "Explicit retry" });
      assert.notEqual(retry.isError, true);
      assert.equal(retry.details.data.summary_id, finalReceipt.summary_id);
      assert.deepEqual(mutations(calls).map(({ path }) => path), ["/sessions", "/observations",
        "/sessions/host-agent-workflows/finalize", "/sessions/host-agent-workflows/finalize"]);
    }, ({ path }, calls) => {
      if (!path.endsWith("/finalize")) return;
      if (calls.filter(({ path }) => path.endsWith("/finalize")).length === 1) throw new TypeError("fetch failed");
      return Response.json({ ...finalReceipt, state: "already_finalized", already_finalized: true });
    });
  });
}

test("Pi finalization accepts a confirmed continuation but not its unconfirmed reservation", async () => {
  const resumedID = "host-agent-workflows:resume:1";
  await fixture(async ({ run, events, ctx, calls }) => {
    ctx.sessionManager.getBranch().push({ type: "custom", customType: "engram-effective-session",
      data: { runtimeID: "host-agent-workflows", effectiveID: resumedID, pending: true, project: "engram" } });
    const unconfirmed = await run("mem_session_finalize", { summary: "Goal: completed" });
    assert.equal(unconfirmed.isError, true);
    assert.match(unconfirmed.content[0].text, /confirmed.*(?:registration|ownership)/);
    assert.deepEqual(mutations(calls), []);
    assert.notEqual((await run("mem_save", { title: "Before", content: "owned continuation" })).isError, true);
    assert.equal(ctx.sessionManager.getBranch().at(-1).data.pending, true);
    const finalized = await run("mem_session_finalize", { summary: "Goal: completed" });
    assert.notEqual(finalized.isError, true);
    assert.deepEqual(finalized.details.data, { ...finalReceipt, session_id: resumedID });
    await events.get("session_shutdown")({}, ctx);
    assert.deepEqual(mutations(calls).map(({ path }) => path),
      ["/sessions", "/observations", "/sessions/" + encodeURIComponent(resumedID) + "/finalize"]);
    assert.equal(calls.find(({ path }) => path === "/sessions").body.id, resumedID);
    assert.equal(calls.find(({ path }) => path === "/observations").body.session_id, resumedID);
    assert.deepEqual(calls.find(({ path }) => path.endsWith("/finalize")).body,
      { project: "engram", summary: "Goal: completed" });
  }, ({ path }) => path.endsWith("/finalize")
    ? Response.json({ ...finalReceipt, session_id: resumedID }) : undefined);
});

for (const lifecycleEvent of ["session_start", "session_shutdown"]) {
  test("Pi rejected finalization retry preserves prior uncertainty through " + lifecycleEvent, async () => {
    await fixture(async ({ run, events, ctx, calls }) => {
      await run("mem_save", { title: "Before", content: "owned" });
      const unknown = await run("mem_session_finalize", { summary: "Original summary" });
      assert.equal(unknown.isError, true);
      assert.equal(unknown.details.outcome, "unknown");
      const rejected = await run("mem_session_finalize", { summary: " " });
      assert.equal(rejected.isError, true);
      assert.equal(rejected.details.http_status, 400);
      assert.equal(rejected.details.data.code, "invalid_arguments");
      assert.equal(rejected.content[0].text, "project and summary must be non-empty strings");
      await events.get(lifecycleEvent)({}, ctx);
      const beforeLateWrite = mutations(calls).length;
      const late = await run("mem_save", { title: "Late", content: "must not save" });
      assert.equal(late.isError, true);
      assert.match(late.content[0].text, /closing/);
      assert.equal(mutations(calls).length, beforeLateWrite);
      const retry = await run("mem_session_finalize", { summary: "Explicit retry" });
      assert.notEqual(retry.isError, true);
      assert.deepEqual(retry.details.data, { ...finalReceipt, state: "already_finalized", already_finalized: true });
      assert.deepEqual(mutations(calls).map(({ path }) => path), ["/sessions", "/observations",
        "/sessions/host-agent-workflows/finalize", "/sessions/host-agent-workflows/finalize",
        "/sessions/host-agent-workflows/finalize"]);
      assert.deepEqual(calls.filter(({ path }) => path.endsWith("/finalize")).map(({ body }) => body),
        ["Original summary", " ", "Explicit retry"].map(summary => ({ summary, project: "engram" })));
    }, ({ path }, calls) => {
      if (!path.endsWith("/finalize")) return;
      const count = calls.filter(({ path }) => path.endsWith("/finalize")).length;
      if (count === 1) throw new TypeError("fetch failed");
      if (count === 2) return Response.json({ code: "invalid_arguments",
        error: "project and summary must be non-empty strings" }, { status: 400 });
      return Response.json({ ...finalReceipt, state: "already_finalized", already_finalized: true });
    });
  });
}

test("Pi first definitive finalization rejection retains ordinary write compatibility", async () => {
  await fixture(async ({ run, calls }) => {
    await run("mem_save", { title: "Before", content: "owned" });
    const rejected = await run("mem_session_finalize", { summary: " " });
    assert.equal(rejected.isError, true);
    assert.equal(rejected.details.http_status, 400);
    assert.equal(rejected.details.data.code, "invalid_arguments");
    assert.notEqual((await run("mem_save", { title: "After", content: "still open" })).isError, true);
    assert.deepEqual(mutations(calls).map(({ path }) => path), ["/sessions", "/observations",
      "/sessions/host-agent-workflows/finalize", "/sessions", "/observations"]);
    assert.deepEqual(calls.filter(({ path }) => path === "/sessions").map(({ body }) => body.id),
      ["host-agent-workflows", "host-agent-workflows"]);
  }, ({ path }) => path.endsWith("/finalize")
    ? Response.json({ code: "invalid_arguments", error: "project and summary must be non-empty strings" }, { status: 400 }) : undefined);
});
