// OpenCode V2 durable prompt identity against a real Engram HTTP server.
// Requires server support for `source_inbox_id` on POST /prompts (#1464).
import assert from "node:assert/strict"
import { mkdtempSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { createInterface } from "node:readline"
import { spawn, spawnSync } from "node:child_process"
import { test } from "node:test"

const PROJECT_ID = "project-1"
const PLUGIN_URL = "http://engram.invalid"

async function bounded(promise, message, ms = 5000) {
  let timer
  try {
    return await Promise.race([promise, new Promise((_, reject) => {
      timer = setTimeout(() => reject(new Error(message)), ms)
    })])
  } finally { clearTimeout(timer) }
}

// Isolated real server on a persistent store directory; stop() closes stdin,
// which is the bridge's shutdown signal.
async function startServer(executable, dir) {
  const child = spawn(executable, [join(dir, "store")], {
    env: {
      ...process.env,
      HOME: dir,
      ENGRAM_DATA_DIR: join(dir, "data"),
      ENGRAM_CLOUD_AUTOSYNC: "0",
      ENGRAM_HTTP_TOKEN: "",
    },
    stdio: ["pipe", "pipe", "pipe"],
  })
  let stderr = ""
  child.stderr.setEncoding("utf8").on("data", (chunk) => { stderr += chunk })
  const lines = createInterface({ input: child.stdout })
  const exited = new Promise((resolve) => child.once("exit", resolve))
  const stop = async () => {
    lines.close()
    if (child.exitCode !== null || child.signalCode !== null) return
    child.stdin.end()
    try { await bounded(exited, `server shutdown timed out: ${stderr}`) }
    catch (error) {
      child.kill()
      await bounded(exited, `server kill timed out: ${stderr}`)
      throw error
    }
  }
  try {
    const url = await bounded(new Promise((resolve, reject) => {
      lines.once("line", resolve)
      child.once("error", reject)
      child.once("exit", (code) => reject(new Error(`server exited ${code}: ${stderr}`)))
    }), `server startup timed out: ${stderr}`, 60000)
    return { url, stop }
  } catch (error) {
    await stop()
    throw error
  }
}

// Each emit resolves once the plugin pulls the next event, which proves the
// previous one was fully handled.
function eventStream() {
  const queued = []
  let waiting
  let pulled
  return {
    subscribe({ signal } = {}) {
      signal?.addEventListener("abort", () => waiting?.({ value: undefined, done: true }))
      return {
        [Symbol.asyncIterator]() {
          return {
            next() {
              pulled?.()
              pulled = undefined
              if (signal?.aborted) return Promise.resolve({ value: undefined, done: true })
              if (queued.length > 0) return Promise.resolve({ value: queued.shift(), done: false })
              return new Promise((resolve) => { waiting = resolve })
            },
            async return() { return { value: undefined, done: true } },
          }
        },
      }
    },
    emit(event) {
      const handled = new Promise((resolve) => { pulled = resolve })
      const resolve = waiting
      waiting = undefined
      if (resolve) resolve({ value: event, done: false })
      else queued.push(event)
      return bounded(handled, `event ${event.type} was not handled`)
    },
  }
}

test("V2 inbox prompts stay idempotent and deleted across real server restarts", async (t) => {
  const dir = mkdtempSync(join(tmpdir(), "engram-opencode-v2-real-"))
  const originalFetch = globalThis.fetch
  const originalEngramURL = process.env.ENGRAM_URL
  let server
  let cleanup
  // One ordered teardown: plugin, fetch bridge, server, then its directory.
  t.after(async () => {
    try {
      await cleanup?.()
    } finally {
      globalThis.fetch = originalFetch
      if (originalEngramURL === undefined) delete process.env.ENGRAM_URL
      else process.env.ENGRAM_URL = originalEngramURL
      try { await server?.stop() }
      finally { rmSync(dir, { recursive: true, force: true }) }
    }
  })
  const executable = join(dir, process.platform === "win32" ? "real-server.exe" : "real-server")
  const build = spawnSync("go", ["build", "-o", executable, "./plugin/opencode/test/support/real-server"], {
    cwd: new URL("../..", import.meta.url), timeout: 120000, encoding: "utf8",
  })
  assert.ifError(build.error)
  assert.equal(build.status, 0, build.stderr)

  server = await startServer(executable, dir)

  // The plugin keeps one stable URL; the fetch bridge follows server restarts.
  // Project detection is stubbed because the temp directory has no git remote.
  const promptResponses = []
  const registrations = []
  globalThis.fetch = async (url, init) => {
    const target = new URL(url)
    if (target.origin !== PLUGIN_URL) return originalFetch(url, init)
    if (target.pathname === "/project/current") {
      return new Response(JSON.stringify({ project: "engram", project_source: "git_remote" }))
    }
    const response = await originalFetch(`${server.url}${target.pathname}${target.search}`, init)
    if (target.pathname === "/sessions" && init?.method === "POST") registrations.push(JSON.parse(init.body))
    if (target.pathname === "/prompts" && init?.method === "POST") {
      promptResponses.push({ ...JSON.parse(init.body), status: response.status, reply: await response.clone().json() })
    }
    return response
  }
  process.env.ENGRAM_URL = PLUGIN_URL

  let events = eventStream()
  const module = await import(new URL("./engram.ts?v2-real-server", import.meta.url).href)
  const setup = () => module.default.setup({
    location: { directory: dir, project: { id: PROJECT_ID } },
    event: { subscribe: (options) => events.subscribe(options) },
    session: {
      async get({ sessionID }) { return { id: sessionID, projectID: PROJECT_ID } },
      hook: async () => ({ dispose: async () => {} }),
    },
    tool: { hook: async () => ({ dispose: async () => {} }) },
  })

  cleanup = await setup()

  const text = "Rotate the staging credentials before the release"
  const enqueue = (inboxID) => events.emit({
    type: "session.inbox.enqueued",
    location: { directory: dir },
    data: { sessionID: "ses_root", inboxID, item: { type: "user", payload: { text }, delivery: "queue" } },
  })
  const prompts = async () => {
    const response = await originalFetch(`${server.url}/prompts/recent?project=engram&limit=100`)
    assert.equal(response.status, 200)
    return response.json()
  }
  const restart = async () => {
    await server.stop()
    server = await startServer(executable, dir)
  }

  await enqueue("msg_inbox_x")
  assert.equal((await prompts()).length, 1, "first admission creates one prompt")
  const promptX = promptResponses[0].reply.id
  assert.equal(promptResponses[0].source_inbox_id, "msg_inbox_x")

  await enqueue("msg_inbox_x")
  assert.equal((await prompts()).length, 1, "replayed admission must not create another prompt")
  assert.equal(promptResponses[1].reply.id, promptX, "replay resolves to the existing prompt")

  await enqueue("msg_inbox_y")
  assert.equal((await prompts()).length, 2, "distinct inbox items with identical text stay distinct")

  await restart()
  await enqueue("msg_inbox_x")
  await enqueue("msg_inbox_y")
  assert.equal((await prompts()).length, 2, "replays after a restart must not create prompts")

  const deleted = await originalFetch(`${server.url}/prompts/${promptX}`, { method: "DELETE" })
  assert.equal(deleted.status, 200, await deleted.text())
  assert.deepEqual((await prompts()).map(({ content }) => content), [text])

  await enqueue("msg_inbox_x")
  assert.equal(promptResponses.at(-1).status, 409, "a deleted inbox identity is refused")
  assert.equal((await prompts()).length, 1, "replay after deletion must not resurrect the prompt")

  await restart()
  await enqueue("msg_inbox_x")
  assert.equal(promptResponses.at(-1).status, 409)
  assert.equal((await prompts()).length, 1, "deletion survives a restart")

  for (let suffix = 2; suffix <= 41; suffix++) {
    await cleanup()
    events = eventStream()
    cleanup = await setup()
    const before = registrations.length
    await enqueue(`msg_resumed_${suffix}`)
    assert.deepEqual(registrations.slice(before), [{ id: "ses_root", project: "engram", directory: dir, resume: true }])
    const captured = promptResponses.at(-1)
    assert.equal(captured.status, 201)
    assert.equal(captured.session_id, `ses_root:resume:${suffix}`)
    assert.equal(captured.source_inbox_id, `msg_resumed_${suffix}`)
    assert.ok((await prompts()).some((prompt) => prompt.session_id === captured.session_id))
  }
})
