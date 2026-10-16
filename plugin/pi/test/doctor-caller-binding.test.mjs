import assert from 'node:assert/strict';
import { test } from 'node:test';
import { importPluginFromSandbox, withPluginSandbox } from './plugin-sandbox.mjs';

const fallback = { status: 'unknown', root_state: 'unknown', effective_state: 'unknown', reason_code: 'caller_binding_unavailable', safe_next_step: 'Verify support and availability of the caller diagnostic.', write_success_guaranteed: false };
const observed = { status: 'ok', root_state: 'ended', effective_state: 'active', reason_code: 'binding_observed', safe_next_step: 'No action required; a later write is not guaranteed.', write_success_guaranteed: false };
for (const scenario of ['mapped', 'blocked', 'missing', 'no-append', 'no-branch', 'throw-branch', 'throw-id', 'throw-getter', 'foreign', 'old-server', 'malformed', 'private-echo', 'transport']) {
  test(`doctor caller binding: ${scenario}`, async () => {
    const originalFetch = globalThis.fetch;
    const env = Object.fromEntries(['ENGRAM_URL', 'ENGRAM_PROJECT'].map(key => [key, process.env[key]]));
    process.env.ENGRAM_URL = 'http://127.0.0.1:17437';
    delete process.env.ENGRAM_PROJECT;
    const calls = [];
    const projectReport = { status: 'ok', checks: [{ name: 'project', status: 'ok' }] };
    const missingEvidence = ['missing', 'throw-id', 'throw-branch', 'foreign'].includes(scenario);
    const assessment = missingEvidence ? { ...fallback, reason_code: 'caller_context_missing', safe_next_step: 'Collect caller context without registering a session.' } : scenario === 'throw-getter' ? { ...fallback, root_state: 'ended', effective_state: 'active', reason_code: 'host_context_missing', safe_next_step: 'Collect caller context without registering a session.' } : scenario === 'blocked' ? { ...observed, status: 'blocked', effective_state: 'ended', reason_code: 'ended_session_without_persistence', safe_next_step: 'Resume through a host that can persist effective identity; do not reopen the ended session.' } : observed;
    globalThis.fetch = async (url, init = {}) => {
      const request = new URL(url);
      calls.push({ path: request.pathname, query: request.search, method: init.method || 'GET', body: init.body && JSON.parse(init.body) });
      if (request.pathname === '/project/current') return Response.json({ project: 'engram' });
      if (request.pathname === '/doctor') return Response.json(projectReport);
      if (request.pathname === '/doctor/caller-binding') {
        if (scenario === 'transport') throw new Error('private transport details');
        if (scenario === 'old-server') return Response.json({ error: 'private server details' }, { status: 404 });
        if (scenario === 'malformed') return Response.json({ ...observed, safe_next_step: 'private path', write_success_guaranteed: true });
        return Response.json({ ...assessment, ...(scenario === 'private-echo' ? { session_id: 'private identity', error: 'private details' } : {}) });
      }
      return Response.json({ status: 'ok' });
    };
    try {
      await withPluginSandbox('doctor-pi-', async ({ sandbox }) => {
        const tools = new Map();
        const api = { registerTool: tool => tools.set(tool.name, tool), on() {}, appendEntry() { assert.fail('doctor appended an entry'); } };
        if (scenario === 'no-append') delete api.appendEntry;
        if (scenario === 'throw-getter') Object.defineProperty(api, 'appendEntry', { get() { throw new Error('private getter'); } });
        (await importPluginFromSandbox(sandbox))(api);
        const entries = [{ type: 'custom', customType: 'engram-effective-session', data: { runtimeID: 'root', effectiveID: 'root:resume:2', project: 'engram' } }];
        const manager = { getSessionId: () => 'root', getBranch: () => entries };
        if (scenario === 'throw-id') manager.getSessionId = () => { throw new Error('private id'); };
        if (scenario === 'throw-branch') manager.getBranch = () => { throw new Error('private branch'); };
        if (scenario === 'no-branch') delete manager.getBranch;
        const ctx = { cwd: sandbox, ui: { setStatus() {} }, ...(scenario === 'missing' ? {} : { sessionManager: manager }) };
        const before = JSON.stringify(entries);
        const result = await tools.get('mem_doctor').execute('doctor', { project: scenario === 'foreign' ? 'other' : 'engram', check: 'storage', runtime_session_id: 'model-id' }, undefined, undefined, ctx);
        assert.notEqual(result.isError, true, JSON.stringify(result));
        const expected = ['old-server', 'malformed', 'transport'].includes(scenario) ? fallback : assessment;
        assert.deepEqual(result.details.data, { ...projectReport, caller_binding: expected });
        assert.doesNotMatch(JSON.stringify(result.details.data.caller_binding), /private|model-id/);
        const caller = calls.find(call => call.path === '/doctor/caller-binding');
        assert.ok(caller);
        assert.equal(caller.method, 'POST');
        assert.equal(caller.query, '');
        assert.equal(caller.body.project, scenario === 'foreign' ? 'other' : 'engram');
        assert.equal(caller.body.runtime_session_id, ['missing', 'throw-id'].includes(scenario) ? undefined : 'root');
        assert.equal(caller.body.effective_session_id, ['missing', 'throw-id', 'throw-branch', 'foreign'].includes(scenario) ? undefined : scenario === 'no-branch' ? 'root' : 'root:resume:2');
        assert.equal(caller.body.host_context.branch_available, scenario === 'missing' ? null : scenario === 'no-branch' ? false : scenario === 'throw-branch' ? null : true);
        assert.equal(caller.body.host_context.append_entry_available, scenario === 'throw-getter' ? null : scenario === 'no-append' ? false : true);
        const rendered = tools.get('mem_doctor').renderResult(result, { expanded: false }, {}, { isError: false });
        assert.match(rendered.text, /Project checks:/);
        assert.match(rendered.text, new RegExp(`Caller binding: ${expected.status}`));
        assert.deepEqual(calls.filter(call => call.method !== 'GET').map(call => call.path), ['/doctor/caller-binding']);
        assert.match(calls.find(call => call.path === '/doctor').query, /check=storage/);
        assert.equal(JSON.stringify(entries), before);
      });
    } finally {
      globalThis.fetch = originalFetch;
      for (const [key, value] of Object.entries(env)) if (value === undefined) delete process.env[key]; else process.env[key] = value;
    }
  });
}
