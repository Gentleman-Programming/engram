import assert from "node:assert/strict";
import { test } from "node:test";
import { runtimeMetadataResponse } from "./support/runtime-contract-fixture.mjs";
import { importPluginFromSandbox, PLUGIN_ROOT, withPluginSandbox } from "./plugin-sandbox.mjs";

for (const scenario of [
  { name: "implicit project", params: {}, expected: "caller-org" },
  { name: "explicit project keeps caller org", params: { project: "other-project" }, expected: "caller-org" },
  { name: "explicit cwd selects its org", params: { cwd: "/other-repo" }, expected: "other-org" },
  { name: "explicit org wins", params: { org: "  explicit-org  " }, expected: "explicit-org" },
  { name: "explicit org wins over cwd", params: { cwd: "/other-repo", org: "explicit-org" }, expected: "explicit-org" },
  { name: "foreign cwd without org never inherits caller org", params: { cwd: "/other-repo" }, otherOrg: null, expected: undefined },
  { name: "blank org inherits", params: { org: "  " }, expected: "caller-org" },
  { name: "no configured org", params: {}, configuredOrg: null, expected: undefined },
  { name: "ambiguous cwd permits explicit project", params: { project: "other-project" }, ambiguous: true, expected: "caller-org" },
  { name: "explicit cwd metadata failure prevents writes", params: { cwd: "/other-repo" }, metadataHTTPFailure: true },
]) {
  test(`native mem_save org: ${scenario.name}`, async () => {
    const originalFetch = globalThis.fetch;
    const originalURL = process.env.ENGRAM_URL;
    process.env.ENGRAM_URL = "http://127.0.0.1:17437";
    const observations = [];
    let projectReads = 0;
    globalThis.fetch = async (url, init = {}) => {
      const request = new URL(url);
      const json = data => new Response(JSON.stringify(data));
      if (request.pathname === "/runtime-contract") return runtimeMetadataResponse(request.pathname);
      if (request.pathname === "/project/current") {
        projectReads++;
        if (projectReads > 1 && scenario.metadataHTTPFailure) return new Response('{"error":"metadata unavailable"}', { status: 503 });
        const other = request.searchParams.get("cwd") === "/other-repo";
        return json({ project: scenario.ambiguous ? "" : other ? "other-project" : "caller-project", project_source: scenario.ambiguous ? "ambiguous" : "config",
          ...(scenario.ambiguous ? { error_hint: "Choose an explicit project", available_projects: ["caller-project", "other-project"] } : {}),
          cwd: request.searchParams.get("cwd"), org: other ? scenario.otherOrg === null ? undefined : "other-org"
            : scenario.configuredOrg === null ? undefined : "caller-org" });
      }
      if (request.pathname === "/health") return json({ capabilities: { isolated_session_registration: true } });
      if (request.pathname === "/sessions") return json({ id: JSON.parse(init.body).id, status: "created" });
      if (request.pathname === "/observations") { observations.push(JSON.parse(init.body)); return json({ id: 1 }); }
      return json({});
    };
    try {
      await withPluginSandbox("engram-org-", async ({ sandbox }) => {
        const tools = new Map();
        (await importPluginFromSandbox(sandbox))({ registerTool(tool) { tools.set(tool.name, tool); }, on() {} });
        const ctx = { cwd: PLUGIN_ROOT, sessionManager: { getSessionId: () => "org-session" }, ui: { setStatus() {} } };
        const result = await tools.get("mem_save").execute("org-save", { title: "Org test", content: "Test content", ...scenario.params }, undefined, undefined, ctx);
        if (scenario.metadataHTTPFailure) {
          assert.equal(result.isError, true);
          assert.equal(observations.length, 0, "org lookup failure must not persist an untagged observation");
          return;
        }
        assert.notEqual(result.isError, true, result.content?.[0]?.text);
        assert.equal(observations.length, 1);
        assert.equal(observations[0].org, scenario.expected);
        assert.equal(projectReads, scenario.params.cwd || scenario.ambiguous ? 2 : 1, "org inheritance reuses existing core detection without another request");
        const metadata = await tools.get("mem_current_project").execute("org-current", {}, undefined, undefined, ctx);
        assert.notEqual(metadata.isError, true);
        assert.equal(metadata.details.data.org, scenario.configuredOrg === null ? undefined : "caller-org");
      });
    } finally {
      globalThis.fetch = originalFetch;
      if (originalURL === undefined) delete process.env.ENGRAM_URL; else process.env.ENGRAM_URL = originalURL;
    }
  });
}
