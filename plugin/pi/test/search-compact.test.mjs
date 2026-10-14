import assert from "node:assert/strict";
import { test } from "node:test";
import { importPluginFromSandbox, withPluginSandbox } from "./plugin-sandbox.mjs";

// Register the public extension and exercise the same tool Pi receives. The shared
// sandbox owns dependency stubs, module isolation, timers and fixture cleanup.
async function withSearch(body) {
  const originalFetch = globalThis.fetch;
  const originalUrl = process.env.ENGRAM_URL;
  process.env.ENGRAM_URL = "http://127.0.0.1:17437";
  const requests = [];
  let payload = [];
  let status = 200;
  globalThis.fetch = async (url) => {
    const request = new URL(url);
    if (request.pathname === "/health") return Response.json({ status: "ok" });
    if (request.pathname === "/project/current") return Response.json({ project: "engram" });
    if (request.pathname === "/search") {
      requests.push(request.pathname + request.search);
      return Response.json(payload, { status });
    }
    throw new Error(`unexpected request: ${url}`);
  };
  try {
    await withPluginSandbox("engram-pi-search-compact-", async ({ dir, sandbox }) => {
      const tools = new Map();
      const register = await importPluginFromSandbox(sandbox);
      register({ registerTool(tool) { tools.set(tool.name, tool); }, on() {} });
      const tool = tools.get("mem_search");
      assert.ok(tool);
      await body({
        tool,
        requests,
        reply(data, code = 200) { payload = data; status = code; },
        execute(params) {
          return tool.execute("search-contract", params, undefined, undefined, {
            cwd: dir,
            sessionManager: { getSessionId: () => "search-contract-session" },
            ui: { setStatus() {} },
          });
        },
      });
    });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalUrl === undefined) delete process.env.ENGRAM_URL;
    else process.env.ENGRAM_URL = originalUrl;
  }
}

test("mem_search exposes response_format as an optional string, leaving semantic validation to Go", async () => {
  await withSearch(async ({ tool }) => {
    // The established sandbox TypeBox stub records public schema constructors.
    const field = tool.parameters.args[0].response_format;
    assert.ok(field, "response_format must be exposed");
    assert.equal(field.kind, "Optional");
    assert.equal(field.args[0].kind, "String");
  });
});

const compact = {
  result: "Found 1 memories.",
  results: [{
    id: 42, sync_id: "sync-42", title: "Search contract", type: "decision",
    state: "active", scope: "project", pinned: true, preview: "A bounded preview…",
    truncated: true, project: "engram", topic_key: "search/contract",
    review_after: "2027-01-01T00:00:00Z", relations: {
      as_source: [{ id: 1, sync_id: "rel-0000000000000001", source_id: "sync-42", target_id: "sync-41", relation: "supersedes", judgment_status: "confirmed", created_at: "2026-10-01T00:00:00Z", updated_at: "2026-10-01T00:00:00Z" }],
      as_target: [{ id: 2, sync_id: "rel-0000000000000002", source_id: "sync-43", target_id: "sync-42", relation: "contradicts", judgment_status: "pending", created_at: "2026-10-02T00:00:00Z", updated_at: "2026-10-02T00:00:00Z" }],
    },
  }],
};

for (const [label, payload] of [
  ["populated", compact],
  ["empty", { result: 'No memories found for: "search contract"', results: [] }],
]) {
  test(`compact ${label} search forwards exact filters and preserves the entire server payload`, async () => {
    await withSearch(async ({ execute, reply, requests }) => {
      reply(payload);
      const result = await execute({ query: "search contract", type: "decision", project: "other", scope: "personal", limit: 7, match_mode: "any", response_format: "compact" });
      assert.deepEqual(requests, ["/search?q=search+contract&type=decision&project=other&scope=personal&limit=7&match_mode=any&response_format=compact"]);
      assert.notEqual(result.isError, true);
      assert.deepEqual(result.details.data, payload);
      assert.deepEqual(JSON.parse(result.content[0].text), payload);
    });
  });
}

test("omitted response_format retains exact legacy query and array response", async () => {
  await withSearch(async ({ execute, reply, requests }) => {
    const legacy = [{ id: 42, content: "Full legacy content", title: "Legacy" }];
    reply(legacy);
    const result = await execute({ query: "legacy", type: "decision", scope: "global", limit: 3, match_mode: "all" });
    assert.deepEqual(requests, ["/search?q=legacy&type=decision&project=engram&scope=global&limit=3&match_mode=all"]);
    assert.notEqual(result.isError, true);
    assert.deepEqual(result.details.data, legacy);
    assert.deepEqual(JSON.parse(result.content[0].text), legacy);
  });
});

for (const response_format of [undefined, "compact"]) {
  test(`all_projects ignores explicit project with ${response_format ?? "omitted"} response_format`, async () => {
    await withSearch(async ({ execute, requests }) => {
      await execute({ query: "recall", project: "ignored", all_projects: true, match_mode: "any", limit: 2, response_format });
      assert.deepEqual(requests, [`/search?q=recall&limit=2&match_mode=any&all_projects=true${response_format ? "&response_format=compact" : ""}`]);
    });
  });
}

for (const [label, params, message, code, expected] of [
  ["invalid format", { query: "bad", response_format: "invalid" }, "invalid response_format", 400, "/search?q=bad&project=engram&response_format=invalid"],
  ["legacy server error", { query: "bad" }, "search failed", 500, "/search?q=bad&project=engram"],
]) {
  test(`${label} propagates through the existing server error path`, async () => {
    await withSearch(async ({ execute, reply, requests }) => {
      reply({ error: message }, code);
      const result = await execute(params);
      assert.deepEqual(requests, [expected]);
      assert.equal(result.isError, true);
      assert.equal(result.content[0].text, message);
      assert.equal(result.details.error, message);
      assert.equal(result.details.http_status, code);
      assert.deepEqual(result.details.data, { error: message });
    });
  });
}
