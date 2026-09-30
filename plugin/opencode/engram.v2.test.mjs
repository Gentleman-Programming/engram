import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import { createRequire, syncBuiltinESMExports } from "node:module"
import { test } from "node:test"

const require = createRequire(import.meta.url)
const childProcess = require("node:child_process")
const fs = require("node:fs")

const source = readFileSync(new URL("./engram.ts", import.meta.url), "utf8")

const DIRECTORY = "/work/engram"
const PROJECT_ID = "project-1"
const INSTANCE_ID = "00000000000000000000000000000000"
let runtimeImport = 0

function httpResponse(data, status = 200) {
  return { ok: status >= 200 && status < 300, status, async text() { return JSON.stringify(data) }, async json() { return data } }
}

// Minimal OpenCode V2 event stream. emit() resolves once the plugin asks for
// the next event, which proves the previous one was fully handled. end() and
// fail() interrupt only the current subscription, like a server restart.
function eventStream() {
  const queued = []
  let waiting
  let waitingReject
  let pulled
  let closed = false
  let interrupt
  let handshake = false
  const subscriptions = []
  const settle = (result) => {
    const resolve = waiting
    const reject = waitingReject
    waiting = undefined
    waitingReject = undefined
    if (result instanceof Error) reject(result)
    else resolve(result)
  }
  return {
    subscriptions,
    subscribe(options) {
      subscriptions.push(options)
      options?.signal?.addEventListener("abort", () => {
        closed = true
        if (waiting) settle({ value: undefined, done: true })
      })
      // OpenCode 2.x opens every subscription with a server.connected handshake.
      let greet = handshake
      return {
        [Symbol.asyncIterator]() {
          return {
            next() {
              if (greet) {
                greet = false
                return Promise.resolve({ value: { type: "server.connected", data: {} }, done: false })
              }
              pulled?.()
              pulled = undefined
              if (interrupt) {
                const result = interrupt
                interrupt = undefined
                return result instanceof Error ? Promise.reject(result) : Promise.resolve(result)
              }
              if (queued.length > 0) return Promise.resolve({ value: queued.shift(), done: false })
              if (closed) return Promise.resolve({ value: undefined, done: true })
              return new Promise((resolve, reject) => {
                waiting = resolve
                waitingReject = reject
              })
            },
            async return() {
              closed = true
              return { value: undefined, done: true }
            },
          }
        },
      }
    },
    emit(event) {
      const handled = new Promise((resolve) => { pulled = resolve })
      if (waiting) settle({ value: event, done: false })
      else queued.push(event)
      return handled
    },
    end() {
      const result = { value: undefined, done: true }
      if (waiting) settle(result)
      else interrupt = result
    },
    fail(error) {
      if (waiting) settle(error)
      else interrupt = error
    },
    withHandshake() {
      handshake = true
    },
    closeForever() {
      closed = true
      if (waiting) settle({ value: undefined, done: true })
    },
  }
}

// Records the reconnect delays the plugin schedules, so backoff is asserted as
// a schedule rather than by counting subscriptions within a real-time window.
const RECONNECT_DELAYS = new Set([50, 100, 200, 400, 800, 1600, 3200, 5000])
function recordReconnectDelays(t) {
  const original = globalThis.setTimeout
  const delays = []
  globalThis.setTimeout = (callback, ms, ...args) => {
    if (RECONNECT_DELAYS.has(ms)) delays.push(ms)
    return original(callback, ms, ...args)
  }
  t.after(() => { globalThis.setTimeout = original })
  return delays
}

function withTimeout(promise, message, ms = 1000) {
  let timer
  const timeout = new Promise((_, reject) => { timer = setTimeout(() => reject(new Error(message)), ms) })
  return Promise.race([promise, timeout]).finally(() => clearTimeout(timer))
}

async function waitFor(condition, message, ms = 1000) {
  const deadline = Date.now() + ms
  while (!condition()) {
    if (Date.now() > deadline) throw new Error(message)
    await new Promise((resolve) => setTimeout(resolve, 5))
  }
}

async function setupV2(t, { sessions = new Map(), promptResponse, registrationResponse, projectResponse, recoveryResponse } = {}) {
  const originalFetch = globalThis.fetch
  const originalBun = globalThis.Bun
  const originalEngramURL = process.env.ENGRAM_URL
  const originalSpawnSync = childProcess.spawnSync
  const originalSpawn = childProcess.spawn
  const originalExistsSync = fs.existsSync
  delete globalThis.Bun
  delete process.env.ENGRAM_URL
  childProcess.spawnSync = () => ({ status: 0, stdout: `${INSTANCE_ID}\n` })
  childProcess.spawn = () => ({ on() { return this }, unref() {} })
  fs.existsSync = () => false
  syncBuiltinESMExports()

  const requests = []
  globalThis.fetch = async (url, init) => {
    const path = new URL(url).pathname
    if (path === "/health") return httpResponse({ status: "ok", instance_id: INSTANCE_ID })
    const body = init?.body ? JSON.parse(init.body) : undefined
    requests.push({ path, method: init?.method, body })
    if (path === "/project/current") return httpResponse(projectResponse ?? { project: "engram", project_source: "git_remote" })
    if (path === "/sessions") return registrationResponse ? registrationResponse(body) : httpResponse({ id: body.id, status: "created" })
    if (path === "/context/compaction") return httpResponse({ context: "previous session context" })
    if (path === "/prompts" && promptResponse) return promptResponse(body)
    if (path.startsWith("/sessions/") && !path.endsWith("/end") && recoveryResponse) return httpResponse(typeof recoveryResponse === "function" ? recoveryResponse(path) : recoveryResponse)
    if (path.endsWith("/end")) return httpResponse({ id: decodeURIComponent(path.split("/")[2]), status: "completed" })
    return httpResponse({})
  }

  t.after(() => {
    globalThis.fetch = originalFetch
    globalThis.Bun = originalBun
    if (originalEngramURL === undefined) delete process.env.ENGRAM_URL
    else process.env.ENGRAM_URL = originalEngramURL
    childProcess.spawnSync = originalSpawnSync
    childProcess.spawn = originalSpawn
    fs.existsSync = originalExistsSync
    syncBuiltinESMExports()
  })

  const hooks = new Map()
  const disposedHooks = []
  const sessionGetIDs = []
  const register = (domain) => async (name, callback) => {
    hooks.set(`${domain}.${name}`, callback)
    return { dispose: async () => { disposedHooks.push(`${domain}.${name}`) } }
  }
  const events = eventStream()
  const ctx = {
    location: { directory: DIRECTORY, project: { id: PROJECT_ID, directory: DIRECTORY, canonical: DIRECTORY } },
    event: { subscribe: (options) => events.subscribe(options) },
    session: {
      hook: register("session"),
      async get({ sessionID }) {
        sessionGetIDs.push(sessionID)
        const info = sessions.get(sessionID)
        if (!info) throw new Error(`session ${sessionID} not found`)
        return info
      },
    },
    tool: { hook: register("tool") },
  }

  runtimeImport += 1
  const module = await import(new URL(`./engram.ts?v2-runtime=${runtimeImport}`, import.meta.url).href)
  const cleanup = await module.default.setup(ctx)
  return {
    module,
    cleanup,
    hooks,
    disposedHooks,
    requests,
    sessionGetIDs,
    events,
    created: (sessionID, parentID) => events.emit({
      type: "session.created",
      data: { sessionID, projectID: PROJECT_ID, location: { directory: DIRECTORY }, ...(parentID ? { parentID } : {}) },
    }),
    deleted: (sessionID) => events.emit({ type: "session.deleted", data: { sessionID } }),
    enqueued: (sessionID, inboxID, item, location) => events.emit({
      type: "session.inbox.enqueued",
      ...(location ? { location } : {}),
      data: { sessionID, inboxID, item },
    }),
    posts: (path) => requests.filter((request) => request.method === "POST" && request.path === path),
  }
}

test("V2 ambiguous recovery retains resumed effective session identity", async (t) => {
  let registrations = 0
  const runtime = await setupV2(t, { sessions: new Map([["root", sessionInfo("root")]]),
    projectResponse: { project: "", project_source: "ambiguous", error_hint: "ambiguous", available_projects: ["repo-a", "repo-b"] },
    recoveryResponse: (path) => ({ id: decodeURIComponent(path.split("/")[2]), project: "repo-a", ownership_mode: "project_owned" }),
    registrationResponse: () => ++registrations === 1 ? httpResponse({ id: "root:resume:2", status: "created" }) : httpResponse({}, 500) })
  const selection = { project: "repo-a", project_choice_reason: "user_selected_after_ambiguous_project", recovery_token: "token" }
  await runtime.hooks.get("tool.execute.before")({ tool: "mem_save", sessionID: "root", input: { ...selection } })
  await runtime.hooks.get("tool.execute.after")({ tool: "mem_save", sessionID: "root", status: "completed", result: { content: "saved" } })
  await runtime.enqueued("root", "resume", { type: "user", payload: { text: "Resume the acknowledged runtime root" } })
  assert.equal(runtime.posts("/prompts").at(-1).body.session_id, "root:resume:2")
  const input = { ...selection, session_id: "invented" }
  await runtime.hooks.get("tool.execute.before")({ tool: "mem_save", sessionID: "root", input })
  assert.deepEqual(input, { ...selection, session_id: "root:resume:2" })
  await runtime.cleanup()
})

test("V2 ambiguous recovery does not authorize another runtime root", async (t) => {
  const runtime = await setupV2(t, { sessions: new Map([["root-a", sessionInfo("root-a")], ["root-b", sessionInfo("root-b")]]),
    projectResponse: { project: "", project_source: "ambiguous", error_hint: "ambiguous", available_projects: ["repo-a", "repo-b"] },
    recoveryResponse: (path) => { const id = decodeURIComponent(path.split("/")[2]); return { id, project: id === "root-a" ? "repo-a" : "repo-b", ownership_mode: "project_owned" } } })
  const before = runtime.hooks.get("tool.execute.before"), after = runtime.hooks.get("tool.execute.after")
  await before({ tool: "mem_save", sessionID: "root-a", input: { project: "repo-a", project_choice_reason: "user_selected_after_ambiguous_project", recovery_token: "token" } })
  await after({ tool: "mem_save", sessionID: "root-a", status: "completed", result: { content: "saved" } })
  const start = runtime.requests.length
  await runtime.enqueued("root-b", "unselected", { type: "user", payload: { text: "Unselected root must not capture a prompt" } })
  await after({ tool: "subagent", sessionID: "root-b", status: "completed", result: { content: "Passive output".repeat(10) } })
  await runtime.hooks.get("session.compaction")({ sessionID: "root-b", system: [] })
  assert.equal(runtime.requests.slice(start).some(({ path, method }) => method === "POST" || path === "/context/compaction"), false)
  await before({ tool: "mem_save", sessionID: "root-b", input: { project: "repo-b", project_choice_reason: "user_selected_after_ambiguous_project", recovery_token: "second-token" } })
  await after({ tool: "mem_save", sessionID: "root-b", status: "completed", result: { content: "saved" } })
  await runtime.enqueued("root-b", "selected", { type: "user", payload: { text: "Separately acknowledged root owns this prompt" } })
  assert.equal(runtime.posts("/prompts").at(-1).body.project, "repo-b")
  await runtime.cleanup()
})

test("V2 ambiguous recovery automatic prompts require matching acknowledgement", async (t) => {
  for (const matching of [false, true]) await t.test(matching ? "matching store" : "different store", async (t) => {
    const runtime = await setupV2(t, { sessions: new Map([["root", sessionInfo("root")]]),
      projectResponse: { project: "", project_source: "ambiguous", error_hint: "ambiguous", available_projects: ["repo-a", "repo-b"] },
      recoveryResponse: { id: "root", project: matching ? "repo-b" : "other", ownership_mode: "project_owned" } })
    await runtime.hooks.get("tool.execute.before")({ tool: "mem_save", sessionID: "root", input: { project: "repo-b", project_choice_reason: "user_selected_after_ambiguous_project", recovery_token: "token" } })
    await runtime.hooks.get("tool.execute.after")({ tool: "mem_save", sessionID: "root", status: "completed", result: { content: "saved" } })
    await runtime.enqueued("root", "recovery-prompt", { type: "user", payload: { text: "Automatic recovery prompt with durable identity" } })
    assert.equal(runtime.posts("/prompts").length, matching ? 1 : 0)
    if (!matching) assert.equal(runtime.posts("/sessions").length, 0)
    await runtime.cleanup()
  })
})

test("V2 ambiguous recovery explicit writes preserve runtime identity and selection", async (t) => {
  const runtime = await setupV2(t, { sessions: new Map([["root", sessionInfo("root")]]),
    projectResponse: { project: "", project_source: "ambiguous", error_hint: "ambiguous project", available_projects: ["repo-a", "repo-b"] } })
  const args = { project: "repo-b", project_choice_reason: "user_selected_after_ambiguous_project", recovery_token: "mcp-token" }
  await runtime.hooks.get("tool.execute.before")({ tool: "mem_save", sessionID: "root", input: args })
  assert.equal(args.session_id, "root")
  assert.equal(args.project, "repo-b")
  assert.equal(args.recovery_token, "mcp-token")
  assert.equal(runtime.posts("/sessions").length, 0)
  await runtime.cleanup()
})

for (const content of ["original", [{ type: "file", uri: "file://fixture", mime: "text/plain" }, { type: "text", text: "original" }]]) {
  test("V2 degraded warning preserves completed content and pending unknown results", async (t) => {
    const runtime = await setupV2(t, { sessions: new Map([["root", sessionInfo("root")]]),
      registrationResponse: () => ({ ok: true, status: 200, async text() { return "{broken" } }) })
    const after = runtime.hooks.get("tool.execute.after")
    const unknown = { tool: "read", sessionID: "root", status: "completed", result: { content: 42 } }
    await after(unknown)
    assert.equal(unknown.result.content, 42)
    const error = { tool: "read", sessionID: "root", status: "error", result: { content: "error" } }
    await after(error)
    assert.equal(error.result.content, "error")
    const original = { content, metadata: { keep: true }, output: { keep: "output" } }
    const call = { tool: "read", sessionID: "root", status: "completed", result: original }
    await after(call)
    assert.notStrictEqual(call.result, original)
    assert.deepEqual(call.result.metadata, original.metadata)
    assert.strictEqual(call.result.output, original.output)
    if (typeof content === "string") assert.match(call.result.content, /original[\s\S]*Engram degraded/)
    else {
      assert.deepEqual(call.result.content.slice(0, -1), content)
      assert.match(call.result.content.at(-1).text, /Engram degraded/)
    }
    await runtime.cleanup()
  })
}

test("V2 failed tool lifecycle registers and renews without consuming pending warnings", async (t) => {
  const runtime = await setupV2(t, {
    sessions: new Map([["root", sessionInfo("root")]]),
    registrationResponse: (body) => httpResponse({ id: body.id === "root" ? "root:resume:2" : body.id, status: "created" }),
    promptResponse: () => httpResponse({}, 503),
  })
  t.after(() => runtime.cleanup())
  const after = runtime.hooks.get("tool.execute.after")
  const result = { content: "unchanged error", metadata: { keep: true } }
  const error = { tool: "subagent", sessionID: "root", status: "error", result, error: new Error("failed") }
  await after(error)
  assert.deepEqual(runtime.sessionGetIDs, ["root"])
  assert.equal(runtime.posts("/sessions").length, 1)
  assert.equal(runtime.posts("/sessions")[0].body.resume, true)
  assert.strictEqual(error.result, result)
  assert.equal(runtime.posts("/observations/passive").length, 0)
  await runtime.enqueued("root", "warning", { type: "user", payload: { text: "Generate a pending transport warning" } })
  await runtime.deleted("root")
  await after(error)
  assert.equal(runtime.posts("/sessions").length, 2, "failed tools renew ended sessions")
  assert.strictEqual(error.result, result)
  assert.equal(error.result.content, "unchanged error")
  const success = { tool: "read", sessionID: "root", status: "completed", result: { content: "success" } }
  await after(success)
  assert.match(success.result.content, /success[\s\S]*Engram degraded/)
  const next = { tool: "read", sessionID: "root", status: "completed", result: { content: "next" } }
  await after(next)
  assert.equal(next.result.content, "next")
})

function sessionInfo(id, parentID) {
  return { id, projectID: PROJECT_ID, ...(parentID ? { parentID } : {}) }
}

test("V2 resumed inbox capture retains durable source identity", async (t) => {
  const runtime = await setupV2(t, {
    sessions: new Map([["ses_root", sessionInfo("ses_root")]]),
    registrationResponse: () => httpResponse({ id: "ses_root:resume:2", status: "created" }),
  })
  await runtime.enqueued("ses_root", "inbox-resumed", { type: "user", payload: { text: "Continue this conversation after restart" }, delivery: "queue" })
  assert.equal(runtime.posts("/prompts")[0].body.session_id, "ses_root:resume:2")
  assert.equal(runtime.posts("/prompts")[0].body.source_inbox_id, "inbox-resumed")
  assert.equal(runtime.posts("/sessions").length, 1)
  assert.equal(runtime.posts("/sessions")[0].body.resume, true)
  await runtime.cleanup()
  assert.equal(runtime.posts("/sessions/ses_root%3Aresume%3A2/end").length, 1)
})

test("default export serves V1 through server and V2 through setup", async () => {
  const module = await import(new URL("./engram.ts?v2-shape", import.meta.url).href)
  assert.equal(module.default.id, "engram")
  assert.strictEqual(module.default.server, module.Engram)
  assert.equal(typeof module.default.setup, "function")
  assert.doesNotMatch(source, /^import\s+(?!type\b)[^\n]*from\s+"@opencode(-ai)?\/plugin"/m, "V1 hosts may lack the V2 SDK")
})

test("V2 setup registers session, tool, and event hooks and cleans them up", async (t) => {
  const runtime = await setupV2(t)
  assert.deepEqual([...runtime.hooks.keys()].sort(), [
    "session.compaction",
    "session.context",
    "tool.execute.after",
    "tool.execute.before",
  ])
  assert.equal(runtime.events.subscriptions.length, 1)
  assert.equal(typeof runtime.cleanup, "function")

  await runtime.created("ses_root")
  await runtime.cleanup()

  assert.equal(runtime.disposedHooks.length, 4)
  assert.equal(runtime.events.subscriptions[0].signal.aborted, true)
  assert.equal(runtime.posts("/sessions/ses_root/end").length, 1, "cleanup ends registered sessions")
})

test("V2 session.created binds root sessions but never child sessions", async (t) => {
  const runtime = await setupV2(t)
  await runtime.created("ses_root")
  await runtime.created("ses_child", "ses_root")

  assert.deepEqual(runtime.posts("/sessions").map(({ body }) => body), [
    { id: "ses_root", project: "engram", directory: DIRECTORY, resume: true },
  ])

  await runtime.deleted("ses_root")
  assert.equal(runtime.posts("/sessions/ses_root/end").length, 1)
})

test("V2 session.updated closes a late-attributed child exactly once", async (t) => {
  const runtime = await setupV2(t)
  t.after(() => runtime.cleanup())
  await runtime.created("ses_child")
  assert.equal(runtime.posts("/sessions").length, 1)

  const update = {
    type: "session.updated",
    data: { sessionID: "ses_child", parentID: "ses_root", projectID: PROJECT_ID },
  }
  await runtime.events.emit(update)
  assert.equal(runtime.posts("/sessions/ses_child/end").length, 1)
  await runtime.events.emit(update)
  await runtime.created("ses_child", "ses_root")
  await runtime.enqueued("ses_child", "child-inbox", userItem("A delegated prompt must remain excluded"))
  await runtime.deleted("ses_child")
  await runtime.cleanup()
  assert.equal(runtime.posts("/sessions/ses_child/end").length, 1)
  assert.equal(runtime.posts("/sessions").length, 1)
  assert.equal(runtime.posts("/prompts").length, 0)
})

test("V2 root updates do not register duplicates or unknown roots", async (t) => {
  const runtime = await setupV2(t)
  t.after(() => runtime.cleanup())
  await runtime.created("ses_root")
  for (const sessionID of ["ses_root", "ses_root", "ses_unknown"]) {
    await runtime.events.emit({ type: "session.updated", data: { sessionID, projectID: PROJECT_ID } })
  }
  assert.deepEqual(runtime.posts("/sessions").map(({ body }) => body.id), ["ses_root"])
  assert.equal(runtime.requests.filter(({ path }) => path.endsWith("/end")).length, 0)
})

test("V2 ignores malformed and foreign session updates without closing roots", async (t) => {
  const runtime = await setupV2(t)
  t.after(() => runtime.cleanup())
  await runtime.created("ses_root")
  for (const data of [
    undefined,
    {},
    { sessionID: 42, parentID: "parent", projectID: PROJECT_ID },
    { sessionID: "", parentID: "parent", projectID: PROJECT_ID },
    { sessionID: "ses_root", parentID: 42, projectID: PROJECT_ID },
    { sessionID: "ses_root", parentID: "parent" },
    { sessionID: "ses_root", parentID: "parent", projectID: "other-project" },
    { sessionID: "ses_root", parentID: "parent", projectID: PROJECT_ID, location: { directory: "/work/other" } },
    { sessionID: "ses_unknown", parentID: "parent", projectID: PROJECT_ID },
  ]) {
    await runtime.events.emit({ type: "session.updated", data })
  }
  assert.equal(runtime.posts("/sessions").length, 1)
  assert.equal(runtime.requests.filter(({ path }) => path.endsWith("/end")).length, 0)
  await runtime.events.emit({ type: "session.updated", data: { sessionID: "ses_root", parentID: "parent", projectID: PROJECT_ID } })
  assert.equal(runtime.posts("/sessions/ses_root/end").length, 1, "valid updates still work after malformed events")
})

test("V2 ignores other locations and leaves unrelated tool input untouched", async (t) => {
  const runtime = await setupV2(t)
  await runtime.events.emit({
    type: "session.created",
    data: { sessionID: "ses_elsewhere", projectID: PROJECT_ID, location: { directory: "/work/other" } },
  })
  assert.equal(runtime.posts("/sessions").length, 0)

  const call = { tool: "bash", sessionID: "ses_root", input: undefined }
  await runtime.hooks.get("tool.execute.before")(call)
  assert.equal(call.input, undefined)
})

test("V2 setup releases earlier registrations when a later one fails", async (t) => {
  const runtime = await setupV2(t)
  await runtime.cleanup()
  const ctx = {
    location: { directory: DIRECTORY, project: { id: PROJECT_ID } },
    event: { subscribe: () => { throw new Error("unexpected subscribe") } },
    session: {
      get: async () => { throw new Error("unexpected get") },
      hook: async (name) => ({ dispose: async () => { disposed.push(name) } }),
    },
    tool: { hook: async () => { throw new Error("tool hooks unavailable") } },
  }
  const disposed = []
  await assert.rejects(runtime.module.default.setup(ctx), /tool hooks unavailable/)
  assert.deepEqual(disposed, ["context", "compaction"])
})

function userItem(text, delivery = "queue") {
  return { type: "user", payload: { text }, delivery }
}

test("V2 captures user inbox items with their durable inbox identity", async (t) => {
  const runtime = await setupV2(t, { sessions: new Map([["ses_root", sessionInfo("ses_root")]]) })
  await runtime.enqueued("ses_root", "msg_inbox_1", userItem("Please remember the <private>token</private> decision", "steer"))

  assert.deepEqual(runtime.posts("/prompts").map(({ body }) => body), [
    { session_id: "ses_root", content: "Please remember the [REDACTED] decision", project: "engram", source_inbox_id: "msg_inbox_1" },
  ])
})

test("V2 ignores non-user inbox items, trivial text, and malformed events", async (t) => {
  const runtime = await setupV2(t, { sessions: new Map([["ses_root", sessionInfo("ses_root")]]) })
  await runtime.enqueued("ses_root", "msg_synthetic", { type: "synthetic", payload: { text: "Synthetic reminder text for the agent" }, delivery: "queue" })
  await runtime.enqueued("ses_root", "msg_compaction", { type: "compaction", payload: {}, delivery: "queue" })
  await runtime.enqueued("ses_root", "msg_move", {
    type: "move",
    payload: { location: { directory: "/work/other" }, projectID: PROJECT_ID },
    delivery: "queue",
  })
  await runtime.enqueued("ses_root", "msg_short", userItem("too short"))
  await runtime.enqueued("ses_root", "", userItem("A prompt without a durable inbox identity"))
  await runtime.events.emit({ type: "session.inbox.enqueued", data: { sessionID: "ses_root", inboxID: "msg_x" } })

  assert.equal(runtime.posts("/prompts").length, 0)
})

test("V2 ignores inbox items from subagent sessions and other locations", async (t) => {
  const runtime = await setupV2(t, {
    sessions: new Map([["ses_root", sessionInfo("ses_root")], ["ses_child", sessionInfo("ses_child", "ses_root")]]),
  })
  await runtime.enqueued("ses_child", "msg_child", userItem("Delegated prompt text written by the parent agent"))
  await runtime.enqueued("ses_root", "msg_elsewhere", userItem("Prompt admitted in another location"), { directory: "/work/other" })
  await runtime.enqueued("ses_unknown", "msg_unknown", userItem("Prompt for a session this instance cannot resolve"))

  assert.equal(runtime.posts("/prompts").length, 0)
})

test("V2 keeps distinct inbox items with identical text distinct", async (t) => {
  const runtime = await setupV2(t, { sessions: new Map([["ses_root", sessionInfo("ses_root")]]) })
  await runtime.enqueued("ses_root", "msg_inbox_1", userItem("Run the full test suite again please"))
  await runtime.enqueued("ses_root", "msg_inbox_2", userItem("Run the full test suite again please"))

  assert.deepEqual(runtime.posts("/prompts").map(({ body }) => body.source_inbox_id), ["msg_inbox_1", "msg_inbox_2"])
})

test("V2 forwards replayed inbox items with the same identity for the server to deduplicate", async (t) => {
  const runtime = await setupV2(t, { sessions: new Map([["ses_root", sessionInfo("ses_root")]]) })
  await runtime.enqueued("ses_root", "msg_inbox_1", userItem("Run the full test suite again please"))
  await runtime.enqueued("ses_root", "msg_inbox_1", userItem("Run the full test suite again please"))

  const prompts = runtime.posts("/prompts").map(({ body }) => body)
  assert.equal(prompts.length, 2, "no in-memory dedup: the server owns replay identity")
  assert.deepEqual(prompts[0], prompts[1])
})

test("V2 treats a deleted inbox identity (409) as a silent no-op without retry", async (t) => {
  const runtime = await setupV2(t, {
    sessions: new Map([["ses_root", sessionInfo("ses_root")]]),
    promptResponse: () => httpResponse({ error: "prompt inbox identity was deleted" }, 409),
  })
  const errors = []
  const originalError = console.error
  const originalWarn = console.warn
  console.error = (...args) => errors.push(args)
  console.warn = (...args) => errors.push(args)
  t.after(() => { console.error = originalError; console.warn = originalWarn })

  await withTimeout(runtime.enqueued("ses_root", "msg_deleted", userItem("A prompt the user already deleted")), "409 stalled the event loop")
  await withTimeout(runtime.created("ses_other"), "event after a 409 was not handled")

  assert.equal(runtime.posts("/prompts").length, 1)
  assert.deepEqual(errors, [])
  assert.equal(runtime.events.subscriptions.length, 1)
})

test("V2 inbox capture redacts a private block that straddles the truncation limit", async (t) => {
  const runtime = await setupV2(t, { sessions: new Map([["ses_root", sessionInfo("ses_root")]]) })
  await runtime.enqueued("ses_root", "msg_inbox_1", userItem(`${"a".repeat(1980)}<private>PIN=42</private> trailing`))

  const prompts = runtime.posts("/prompts").map(({ body }) => body)
  assert.equal(prompts.length, 1)
  assert.equal(JSON.stringify(prompts[0]).includes("PIN=42"), false)
  assert.equal(prompts[0].content.includes("[REDACTED]"), true)
})

test("V2 tool hooks bind Engram writes to the root session and capture subagent output", async (t) => {
  const runtime = await setupV2(t, {
    sessions: new Map([["ses_root", sessionInfo("ses_root")], ["ses_child", sessionInfo("ses_child", "ses_root")]]),
  })
  const call = { tool: "engram_mem_save", sessionID: "ses_child", agent: "build", messageID: "msg_1", id: "call_1", input: { title: "x" } }
  await runtime.hooks.get("tool.execute.before")(call)
  assert.equal(call.input.session_id, "ses_root")
  assert.deepEqual(runtime.sessionGetIDs, ["ses_child", "ses_root"])

  const output = "Subagent finished: the auth middleware now validates JWT expiry before routing."
  await runtime.hooks.get("tool.execute.after")({
    tool: "subagent",
    sessionID: "ses_root",
    agent: "build",
    messageID: "msg_2",
    id: "call_2",
    input: {},
    status: "completed",
    result: { content: [{ type: "text", text: output }] },
  })
  assert.deepEqual(runtime.posts("/observations/passive").map(({ body }) => body), [
    { session_id: "ses_root", content: output, project: "engram", source: "task-complete" },
  ])
})

test("V2 tool hook captures subagent output returned as string content", async (t) => {
  const runtime = await setupV2(t, { sessions: new Map([["ses_root", sessionInfo("ses_root")]]) })
  const output = "Subagent finished: the rendered transcript arrives as a plain string."
  await runtime.hooks.get("tool.execute.after")({
    tool: "subagent",
    sessionID: "ses_root",
    agent: "build",
    messageID: "msg_3",
    id: "call_3",
    input: {},
    status: "completed",
    result: { content: output, output: { ignored: true } },
  })
  assert.deepEqual(runtime.posts("/observations/passive").map(({ body }) => body), [
    { session_id: "ses_root", content: output, project: "engram", source: "task-complete" },
  ])
})

test("V2 tool hook captures a plain string output without JSON quoting", async (t) => {
  const runtime = await setupV2(t, { sessions: new Map([["ses_root", sessionInfo("ses_root")]]) })
  const output = "Subagent finished: only a plain string output was returned by the tool."
  await runtime.hooks.get("tool.execute.after")({
    tool: "subagent",
    sessionID: "ses_root",
    agent: "build",
    messageID: "msg_4",
    id: "call_4",
    input: {},
    status: "completed",
    result: { content: "", output },
  })
  assert.deepEqual(runtime.posts("/observations/passive").map(({ body }) => body), [
    { session_id: "ses_root", content: output, project: "engram", source: "task-complete" },
  ])
})

test("V2 tool hook rejects Engram writes without an authoritative session", async (t) => {
  const runtime = await setupV2(t)
  const call = { tool: "engram_mem_save", sessionID: "ses_missing", input: {} }
  await assert.rejects(runtime.hooks.get("tool.execute.before")(call), /authoritative OpenCode runtime session/)
  assert.equal(call.input.session_id, undefined)
})

test("V2 context hook appends memory instructions to the last system part", async (t) => {
  const runtime = await setupV2(t)
  const request = { sessionID: "ses_root", system: [{ type: "text", text: "base" }], messages: [] }
  await runtime.hooks.get("session.context")(request)
  assert.equal(request.system.length, 1)
  assert.match(request.system[0].text, /^base\n\n## Engram Persistent Memory/)

  const empty = { sessionID: "ses_root", system: [], messages: [] }
  await runtime.hooks.get("session.context")(empty)
  assert.equal(empty.system.length, 1)
  assert.equal(empty.system[0].type, "text")
  assert.match(empty.system[0].text, /^## Engram Persistent Memory/)
})

test("V2 compaction hook injects session context and the summary instruction", async (t) => {
  const runtime = await setupV2(t, { sessions: new Map([["ses_root", sessionInfo("ses_root")]]) })
  const compaction = { sessionID: "ses_root", system: [{ type: "text", text: "summarize" }], messages: [] }
  await runtime.hooks.get("session.compaction")(compaction)

  assert.equal(compaction.system.length, 1)
  assert.match(compaction.system[0].text, /^summarize\n\nprevious session context\n\nCRITICAL INSTRUCTION FOR COMPACTED SUMMARY/)
  assert.match(compaction.system[0].text, /Use project: 'engram'/)
  assert.equal(runtime.requests.filter(({ path }) => path === "/context/compaction").length, 1)
})

test("V2 re-subscribes when the event stream ends", async (t) => {
  const runtime = await setupV2(t)
  runtime.events.end()
  await waitFor(() => runtime.events.subscriptions.length === 2, "plugin did not re-subscribe after the stream ended")

  await withTimeout(runtime.created("ses_root"), "event after re-subscription was not handled")
  assert.equal(runtime.posts("/sessions").length, 1)
  await runtime.cleanup()
})

test("V2 re-subscribes when the event stream throws", async (t) => {
  const runtime = await setupV2(t)
  runtime.events.fail(new Error("stream reset"))
  await waitFor(() => runtime.events.subscriptions.length === 2, "plugin did not re-subscribe after the stream failed")

  await withTimeout(runtime.created("ses_root"), "event after re-subscription was not handled")
  assert.equal(runtime.posts("/sessions").length, 1)
  await runtime.cleanup()
})

test("V2 keeps listening when handling one event throws", async (t) => {
  const runtime = await setupV2(t)
  const poisoned = { type: "session.created", get data() { throw new Error("malformed event") } }
  await withTimeout(runtime.events.emit(poisoned), "loop stopped pulling after a failing event")
  await withTimeout(runtime.created("ses_root"), "event after a failing event was not handled")

  assert.equal(runtime.posts("/sessions").length, 1)
  assert.equal(runtime.events.subscriptions.length, 1, "a failing event must not drop the subscription")
  await runtime.cleanup()
})

test("V2 backs off between re-subscriptions and stops after cleanup", async (t) => {
  const delays = recordReconnectDelays(t)
  const runtime = await setupV2(t)
  runtime.events.closeForever()
  try {
    await waitFor(() => delays.length >= 3, "plugin did not keep re-subscribing", 5000)
    assert.deepEqual(delays.slice(0, 3), [50, 100, 200])
  } finally {
    await withTimeout(runtime.cleanup(), "cleanup waited on the reconnect backoff")
  }
  const attempts = runtime.events.subscriptions.length
  await new Promise((resolve) => setTimeout(resolve, 20))
  assert.equal(runtime.events.subscriptions.length, attempts, "no re-subscription after cleanup")
})

test("V2 keeps backing off when each subscription only delivers the server handshake", async (t) => {
  const delays = recordReconnectDelays(t)
  const runtime = await setupV2(t)
  runtime.events.withHandshake()
  runtime.events.closeForever()
  try {
    await waitFor(() => delays.length >= 3, "plugin did not keep re-subscribing", 5000)
    assert.deepEqual(delays.slice(0, 3), [50, 100, 200], "server.connected must not reset the backoff")
  } finally {
    await withTimeout(runtime.cleanup(), "cleanup waited on the reconnect backoff")
  }
})
