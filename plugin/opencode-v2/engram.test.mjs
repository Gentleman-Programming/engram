import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, mkdir, copyFile, writeFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

const source = resolve('plugin/opencode-v2/engram.ts')

async function harness(t, { localIdentity = false } = {}) {
  const root = await mkdtemp(join(tmpdir(), 'engram-v2-hook-'))
  const previousURL = process.env.ENGRAM_URL
  const previousFetch = globalThis.fetch
  const previousTimeout = globalThis.__engramIdentityTimeout
  if (localIdentity) delete process.env.ENGRAM_URL
  else process.env.ENGRAM_URL = 'http://127.0.0.1:1'
  const requests = []
  globalThis.fetch = async (url, options = {}) => {
    const path = new URL(url).pathname
    const body = options.body ? JSON.parse(options.body) : undefined
    requests.push({ path, body })
    const old = new Date(Date.now() - 60 * 60 * 1000).toISOString()
    const value = path === '/health' ? { instance_id: 'a'.repeat(32) }
      : path === '/project/current' ? { project: 'example' }
      : path.startsWith('/sessions/') ? { started_at: old }
      : path === '/observations' ? [{ created_at: old }]
      : {}
    return { ok: true, json: async () => value }
  }
  let cleanup
  t.after(async () => {
    if (cleanup) await cleanup()
    globalThis.fetch = previousFetch
    if (previousTimeout === undefined) delete globalThis.__engramIdentityTimeout
    else globalThis.__engramIdentityTimeout = previousTimeout
    if (previousURL === undefined) delete process.env.ENGRAM_URL
    else process.env.ENGRAM_URL = previousURL
    await rm(root, { recursive: true, force: true })
  })
  const mod = join(root, 'node_modules', '@opencode', 'plugin')
  await mkdir(mod, { recursive: true })
  await writeFile(join(root, 'package.json'), '{"type":"module"}')
  await writeFile(join(mod, 'package.json'), '{"name":"@opencode/plugin","type":"module","exports":"./index.js"}')
  await writeFile(join(mod, 'index.js'), 'export const Plugin = { define: value => value }')
  const target = join(root, 'engram.ts')
  if (localIdentity) {
    const { readFile } = await import('node:fs/promises')
    const code = await readFile(source, 'utf8')
    await writeFile(target, code.replace(
      'import { spawn, spawnSync } from "node:child_process"',
      `import { spawn } from "node:child_process"\nconst spawnSync = (_bin, _args, options) => {\n  globalThis.__engramIdentityTimeout = options.timeout\n  return { status: null, stdout: "", error: new Error("ETIMEDOUT") }\n}`,
    ))
  } else await copyFile(source, target)
  const { default: plugin } = await import(pathToFileURL(target).href)
  const hooks = new Map()
  let deliver
  const events = {
    async *[Symbol.asyncIterator]() {
      while (true) {
        const event = await new Promise(resolve => { deliver = resolve })
        if (event === null) return
        yield event.value
        event.done()
      }
    },
  }
  const ctx = {
    location: { directory: root, project: { id: 'project-id' } },
    session: {
      get: async ({ sessionID }) => ({ id: sessionID, projectID: 'project-id', ...(sessionID === 'child' ? { parentID: 'root' } : {}) }),
      hook: async (name, fn) => { hooks.set(name, fn) },
    },
    tool: { hook: async (name, fn) => { hooks.set(name, fn) } },
    event: { subscribe: () => events },
  }
  cleanup = await plugin.setup(ctx)
  async function emit(value) {
    while (!deliver) await new Promise(resolve => setImmediate(resolve))
    const send = deliver
    deliver = undefined
    await new Promise(resolve => send({ value, done: resolve }))
    // The iterator resumes after the asynchronous handler finishes.
    while (!deliver) await new Promise(resolve => setImmediate(resolve))
  }
  return { requests, hooks, emit }
}

function admitted(sessionID, inboxID, text, type = 'user') {
  return { type: 'session.inbox.enqueued', data: { sessionID, inboxID, item: { type, payload: { text } } } }
}

test('V2 captures only admitted root user inbox messages and retains stable identity', async t => {
  const { requests, hooks, emit } = await harness(t)
  assert.equal(hooks.has('prompt'), false)
  await emit({ type: 'prompt', data: { sessionID: 'root', text: 'Unadmitted prompt with text' } })
  assert.equal(requests.filter(r => r.path === '/prompts').length, 0)
  await emit({ type: 'session.created', data: { sessionID: 'root', projectID: 'project-id' } })
  await emit(admitted('root', 'one', 'A useful prompt with text'))
  await emit(admitted('root', 'two', 'A useful prompt with text'))
  await emit(admitted('root', 'three', 'A tool output with text', 'tool'))
  await emit(admitted('child', 'four', 'A child prompt with text'))
  const prompts = requests.filter(r => r.path === '/prompts').map(r => r.body)
  assert.deepEqual(prompts.map(p => p.source_inbox_id), ['one', 'two'])
  assert.deepEqual(prompts.map(p => p.content), ['A useful prompt with text', 'A useful prompt with text'])
  assert.ok(prompts.every(p => p.session_id === 'root' && p.project === 'example'))
  // Duplicate delivery currently reaches HTTP; durable dedup belongs to the Go store.
  await emit(admitted('root', 'one', 'A useful prompt with text'))
  assert.equal(requests.filter(r => r.path === '/prompts').length, 3)
  assert.deepEqual(requests.filter(r => r.path === '/sessions/root/end').length, 0)
})

test('V2 child context before creation keeps protocol but never receives root nudge', async t => {
  const { hooks, requests } = await harness(t)
  const childSystem = [{ type: 'text', text: 'Existing instructions' }]
  await hooks.get('context')({ sessionID: 'child', system: childSystem })
  assert.match(childSystem[0].text, /Engram Persistent Memory/)
  assert.doesNotMatch(childSystem[0].text, /MEMORY REMINDER/)
  assert.equal(requests.filter(r => r.path === '/sessions/child').length, 0)
  const rootSystem = [{ type: 'text', text: 'Existing instructions' }]
  await hooks.get('context')({ sessionID: 'root', system: rootSystem })
  assert.match(rootSystem[0].text, /MEMORY REMINDER/)
})

test('V2 strips private spans and truncates admitted text before HTTP', async t => {
  const { requests, emit } = await harness(t)
  await emit(admitted('root', 'redacted', `before <private>never send this</private> ${'x'.repeat(2100)}`))
  const prompts = requests.filter(r => r.path === '/prompts')
  assert.equal(prompts.length, 1)
  assert.ok(prompts[0].body.content.includes('[REDACTED]'))
  assert.ok(!prompts[0].body.content.includes('never send this'))
  assert.ok(prompts[0].body.content.length <= 2003)
})

test('V2 redacts a private span crossing the prompt limit before HTTP', async t => {
  const { requests, emit } = await harness(t)
  await emit(admitted('root', 'crossing', `${'x'.repeat(1980)}<private>secret-crossing-limit${'y'.repeat(50)}</private> public`))
  const content = requests.find(r => r.path === '/prompts').body.content
  assert.ok(content.includes('[REDACTED]'))
  assert.ok(!content.includes('secret-crossing-limit'))
  assert.ok(content.length <= 2003)
})

test('V2 bounds local identity lookup and degrades when it times out', async t => {
  const { requests, emit } = await harness(t, { localIdentity: true })
  await emit(admitted('root', 'timeout', 'A prompt that must not block on local identity'))
  assert.equal(requests.filter(r => r.path === '/prompts').length, 0)
  assert.ok(Number.isFinite(globalThis.__engramIdentityTimeout))
  assert.ok(globalThis.__engramIdentityTimeout > 0 && globalThis.__engramIdentityTimeout <= 5000)
})
