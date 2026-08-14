import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import { test } from "node:test"

const source = readFileSync(new URL("./engram.ts", import.meta.url), "utf8")

function extractFunctionBody(name) {
  const signature = source.indexOf(`function ${name}`)
  assert.notEqual(signature, -1, `${name} signature not found`)
  const bodyStart = source.indexOf("{", signature)
  let depth = 0
  for (let index = bodyStart; index < source.length; index += 1) {
    if (source[index] === "{") depth += 1
    if (source[index] === "}") depth -= 1
    if (depth === 0) return source.slice(bodyStart + 1, index)
  }
  throw new Error(`${name} body not found`)
}

function buildEnsureSession(engramFetch) {
  const body = extractFunctionBody("ensureSession")
  const factory = new Function("knownSessions", "subAgentSessions", "engramFetch", "project", "ctx", `
    return async function ensureSession(sessionId) {${body}}
  `)
  const knownSessions = new Set()
  return { ensureSession: factory(knownSessions, new Set(), engramFetch, "engram", { directory: "/work/engram" }), knownSessions }
}

test("registration enters the cache only after a successful acknowledgement", async () => {
  assert.match(source, /signal: AbortSignal\.timeout\(3000\)/)
  let calls = 0
  const { ensureSession, knownSessions } = buildEnsureSession(async () => {
    calls += 1
    return calls === 1 ? null : { status: "created" }
  })

  assert.equal(await ensureSession("runtime"), false)
  assert.equal(knownSessions.has("runtime"), false)
  assert.equal(await ensureSession("runtime"), true)
  assert.equal(knownSessions.has("runtime"), true)
  assert.equal(await ensureSession("runtime"), true)
  assert.equal(calls, 2, "failed acknowledgement must remain retryable; successful acknowledgement must be cached")
})

test("write tool hook binds only the four attributed writes to authoritative runtime identity", () => {
  assert.match(source, /SESSION_ATTRIBUTED_WRITE_TOOLS = new Set\(\[[\s\S]*"mem_save"[\s\S]*"mem_save_prompt"[\s\S]*"mem_session_summary"[\s\S]*"mem_capture_passive"/)
  assert.match(source, /"tool.execute.before"/)
  assert.match(source, /output\.args\.session_id = authoritativeSessionID/)
  assert.match(source, /delete output\.args\.session_id/)
  assert.doesNotMatch(source, /knownSessions\.add\(sessionId\)[\s\S]{0,160}await engramFetch\("\/sessions"/)
})

test("subagent sessions resolve to the authoritative parent and never register themselves", () => {
  assert.match(source, /parentSessions\.set\(sessionId, parentID\)/)
  assert.match(source, /resolveAuthoritativeSessionID/)
  assert.match(source, /while \(parentSessions\.has\(current\)\)/)
})

test("runtime hook retries registration, overwrites model identity, and binds subagents to parent", async () => {
  const originalFetch = globalThis.fetch
  const originalBun = globalThis.Bun
  let registrations = 0
  const registeredIDs = []
  globalThis.Bun = {
    spawnSync(args) {
      if (args.includes("remote")) return { exitCode: 1, stdout: Buffer.from("") }
      return { exitCode: 0, stdout: Buffer.from("/work/engram\n") }
    },
    spawn() {},
    file() { return { async exists() { return false } } },
  }
  globalThis.fetch = async (url, init) => {
    const path = new URL(url).pathname
    if (path === "/health") return { ok: true, async json() { return { status: "ok" } } }
    if (path === "/sessions") {
      registrations += 1
      registeredIDs.push(JSON.parse(init.body).id)
      if (registrations === 1) return { ok: false, async json() { return { error: "unavailable" } } }
      return { ok: true, async json() { return { status: "created" } } }
    }
    return { ok: true, async json() { return {} } }
  }

  try {
    const moduleURL = new URL(`./engram.ts?runtime=${Date.now()}`, import.meta.url)
    const { Engram } = await import(moduleURL.href)
    const plugin = await Engram({ directory: "/work/engram" })
    const before = plugin["tool.execute.before"]

    const first = { args: { session_id: "model-invented" } }
    await before({ tool: "mem_save", sessionID: "runtime" }, first)
    assert.equal(first.args.session_id, undefined)

    const second = { args: { session_id: "model-invented" } }
    await before({ tool: "mem_save", sessionID: "runtime" }, second)
    assert.equal(second.args.session_id, "runtime")
    assert.deepEqual(registeredIDs, ["runtime", "runtime"])

    await plugin.event({ event: { type: "session.created", properties: { info: { id: "sub", parentID: "runtime" } } } })
    const subagent = { args: { session_id: "sub" } }
    await before({ tool: "mem_session_summary", sessionID: "sub" }, subagent)
    assert.equal(subagent.args.session_id, "runtime")
    assert.equal(registrations, 2, "subagent must reuse the confirmed parent, not register itself")

    await plugin.event({ event: { type: "session.created", properties: { info: { id: "orphan-sub", title: "Task (orphan subagent)" } } } })
    const orphan = { args: { session_id: "orphan-sub" } }
    await before({ tool: "mem_capture_passive", sessionID: "orphan-sub" }, orphan)
    assert.equal(orphan.args.session_id, undefined)
    assert.equal(registrations, 2)
  } finally {
    globalThis.fetch = originalFetch
    globalThis.Bun = originalBun
  }
})
