// Disposable synthetic hook host, NOT Pi RPC or the installed runner.
// Load repository adapter in memory: no sandbox files or dependency installation.
import { readFile } from 'node:fs/promises';
import { stripTypeScriptTypes } from 'node:module';
import { pathToFileURL, fileURLToPath } from 'node:url';

const endpoint = process.argv[2];
const identity = process.argv[3];
if (!/^http:\/\/127\.0\.0\.1:\d+$/.test(endpoint) || !identity.startsWith('synthetic-1632-')) throw new Error('unsafe fixture arguments');
process.env.ENGRAM_URL = endpoint;
const sourceURL = new URL('../../index.ts', import.meta.url);
let source = stripTypeScriptTypes(await readFile(sourceURL, 'utf8'));
source = source.replace(/import \{ Text \} from "@earendil-works\/pi-tui";/, 'class Text { constructor(text) { this.text = text; } }');
source = source.replace(/import \{ Type \} from "typebox";/, 'const Type = new Proxy({}, { get: (_, key) => (...args) => ({key, args}) });');
source = source.replace(/from "(\.\/[^\"]+)"/g, (_, relative) => `from ${JSON.stringify(new URL(relative, sourceURL).href)}`);
const { default: register } = await import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`);
const hooks = new Map();
const tools = new Map();
register({ on(name, fn) { hooks.set(name, fn); }, registerTool(tool) { tools.set(tool.name, tool); } });
const ctx = { cwd: fileURLToPath(new URL('../../', import.meta.url)), sessionManager: { getSessionId: () => identity }, ui: { setStatus() {} } };
let sequence = 0;
const event = (name) => process.send?.({ name, seq: sequence++, at: Date.now() });
// Explicit registration avoids startup discovery/spawn hooks and uses no store.
const result = await tools.get('mem_save').execute('synthetic-registration', { title: 'disposable', content: 'synthetic fixture only' }, undefined, undefined, ctx);
if (result.isError) throw new Error(JSON.stringify(result));
event('registered');
let closing = false;
async function shutdown() {
  if (closing) return;
  closing = true;
  event('hook_enter');
  await hooks.get('session_shutdown')({}, ctx);
  event('hook_resolved');
  process.disconnect();
}
// IPC explicitly starts a hook on Windows; it does not model POSIX SIGTERM.
process.on('message', (message) => { if (message === 'shutdown') void shutdown(); });
process.on('SIGTERM', () => { event('sigterm_hook'); void shutdown(); });
