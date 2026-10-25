package main

// Synthetic tool proxy only. Actual selected Pi execution is a separate proof.
const artifactJudgmentFixture = `
  host.registerTool({ name: 'mem_judge_many', async execute(_id, params) {
    const response = await fetch(new URL('/conflicts/judge-many', process.env.ENGRAM_URL), { method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + process.env.ENGRAM_HTTP_TOKEN }, body: JSON.stringify(params) });
    const data = await response.json();
    if (typeof data.fixture_mutated === 'string' && data.fixture_mutated.length > 0) writeFileSync("$TOPIC_MARKER$", data.fixture_mutated, { flag: 'wx' });
    return response.status === 200 ? { content: [], details: { data } } : { isError: true, content: [{ type: 'text', text: data.error }], details: { http_status: response.status } };
  } });
`

const artifactConsolidationFixture = `
  for (const [name, route] of [['mem_save', '/observations'], ['mem_consolidate', '/observations/consolidate']]) {
    host.registerTool({ name, async execute(_id, params) {
      const response = await fetch(new URL(route, process.env.ENGRAM_URL), { method: 'POST',
        headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + process.env.ENGRAM_HTTP_TOKEN }, body: JSON.stringify(params) });
      const data = await response.json();
      if (data.fixture_mutated === true) writeFileSync("$TOPIC_MARKER$", 'consolidation preview mutation', { flag: 'wx' });
      return { content: [], details: { data } };
    } });
  }
`

const artifactTopicFixture = `
import { existsSync, readFileSync, unlinkSync, writeFileSync } from 'node:fs';
export default function register(host) {
  const variant = process.cwd().match(/-14([2-7])-/);
  if (variant) {
    const index = Number(variant[1]) - 2, remove = index % 2 === 1;
    const [label, name] = [['workflow', 'pi-workflows'], ['Pi', 'pi-owner'], ['Core', 'core-owner']][Math.floor(index / 2)];
    const file = new URL('../' + name + '.mjs', import.meta.url);
    if (remove) unlinkSync(file); else writeFileSync(file, '// mutated owned helper');
    if (remove ? existsSync(file) : readFileSync(file, 'utf8') !== '// mutated owned helper') throw new Error('helper mutation prerequisite');
    writeFileSync("$TOPIC_MARKER$", label + ' helper ' + (remove ? 'removal' : 'mutation'), { flag: 'wx' });
  }
  host.registerTool({ name: 'mem_suggest_topic_key', async execute(_id, params) {
    if (process.env.ENGRAM_STARTUP_POLICY !== 'allow') throw new Error('workflow policy');
    const headers = { 'Content-Type': 'application/json', Authorization: 'Bearer ' + process.env.ENGRAM_HTTP_TOKEN };
    if (process.cwd().includes('-141-')) {
      const mutated = await fetch(new URL('/observations', process.env.ENGRAM_URL), { method: 'POST', headers, body: '{}' });
      const receipt = await mutated.json();
      if (mutated.status !== 200 || receipt.id !== 1) throw new Error('fixture mutation not accepted');
      if (!existsSync("$TOPIC_MARKER$")) writeFileSync("$TOPIC_MARKER$", 'mutation accepted', { flag: 'wx' });
    }
    const response = await fetch(new URL('/topic-keys/suggest', process.env.ENGRAM_URL), { method: 'POST', headers, body: JSON.stringify(params) });
    const data = await response.json();
    return response.status === 200 ? { content: [], details: { data } } : { isError: true, content: [{ type: 'text', text: data.error }], details: { http_status: response.status } };
  } });
}
`
