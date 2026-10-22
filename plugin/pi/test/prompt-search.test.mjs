import assert from 'node:assert/strict';
import { test } from 'node:test';
import { importPluginFromSandbox, PLUGIN_ROOT, withPluginSandbox } from './plugin-sandbox.mjs';

// This checks the Pi projection; the Go transport tests exercise a real HTTP handler.
test('prompt search forwards scope and preserves original text without changing observation search', async () => {
  const originalFetch = globalThis.fetch;
  const keys = ['ENGRAM_URL', 'ENGRAM_PROJECT', 'GENTLE_PI_AGENTS_CHILD'];
  const env = Object.fromEntries(keys.map(key => [key, process.env[key]]));
  process.env.ENGRAM_URL = 'http://127.0.0.1:17437';
  delete process.env.ENGRAM_PROJECT;
  delete process.env.GENTLE_PI_AGENTS_CHILD;
  const requests = [];
  const rows = [{ id: 42, session_id: 'stored-child-session', project: 'other', created_at: '2026-10-21 12:00:00', content: 'orbit\n' + 'original 世界 '.repeat(200) }];
  let failure = false;
  globalThis.fetch = async url => {
    const parsed = new URL(url);
    if (parsed.pathname === '/project/current') return new Response('{"project":"engram"}');
    if (parsed.pathname === '/prompts/search' || parsed.pathname === '/search') {
      requests.push(parsed);
      return failure ? new Response('{"error":"search unavailable"}', { status: 500 }) : Response.json(rows);
    }
    throw new Error(`unexpected request: ${url}`);
  };
  try {
    await withPluginSandbox('engram-prompt-search-', async ({ sandbox }) => {
      const tools = new Map();
      const register = await importPluginFromSandbox(sandbox);
      register({ registerTool(tool) { tools.set(tool.name, tool); }, on() {} });
      const tool = tools.get('mem_search_prompts');
      assert.ok(tool, 'dedicated prompt tool must be registered');
      assert.deepEqual(Object.keys(tool.parameters.args[0]).sort(), ['all_projects','limit','project','query']);
      const ctx = { cwd: PLUGIN_ROOT, sessionManager: { getSessionId: () => 'prompt-reader' }, ui: { setStatus() {} } };
      for (const [params, expected] of [
        [{ query: 'orbit' }, { q: 'orbit', project: 'engram' }],
        [{ query: 'orbit', project: 'other', limit: 2 }, { q: 'orbit', project: 'other', limit: '2' }],
        [{ query: 'orbit', all_projects: true }, { q: 'orbit', all_projects: 'true' }],
        // Preserve both selectors so Go rejects the conflict, rather than silently broadening scope.
        [{ query: 'orbit', project: 'other', all_projects: true }, { q: 'orbit', project: 'other', all_projects: 'true' }],
      ]) {
        const result = await tool.execute('prompt-search', params, undefined, undefined, ctx);
        assert.notEqual(result.isError, true);
        assert.deepEqual(result.details.data, rows);
        assert.deepEqual(JSON.parse(result.content[0].text), rows);
        assert.equal(requests.at(-1).pathname, '/prompts/search');
        assert.deepEqual(Object.fromEntries(requests.at(-1).searchParams), expected);
      }
      failure = true;
      const failed = await tool.execute('failed-search', { query: 'orbit' }, undefined, undefined, ctx);
      assert.equal(failed.isError, true);
      assert.match(failed.content[0].text, /search unavailable/);
      failure = false;
      await tools.get('mem_search').execute('observation-search', { query: 'orbit', all_projects: true, project: 'other' }, undefined, undefined, ctx);
      assert.equal(requests.at(-1).pathname, '/search');
      assert.equal(requests.at(-1).searchParams.has('project'), false, 'legacy observation selector precedence is unchanged');
    });
  } finally {
    globalThis.fetch = originalFetch;
    for (const key of keys) { if (env[key] === undefined) delete process.env[key]; else process.env[key] = env[key]; }
  }
});
