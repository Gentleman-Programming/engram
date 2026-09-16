import assert from "node:assert/strict"
import test from "node:test"

import { shouldNudgeForObservations } from "./engram.ts"

const nowSecs = 1_735_689_600

test("allows the first-save nudge only after the 15-minute session threshold", () => {
  assert.equal(shouldNudgeForObservations(true, [], nowSecs, nowSecs - 899), false)
  assert.equal(shouldNudgeForObservations(true, [], nowSecs, nowSecs - 900), true)
  assert.equal(shouldNudgeForObservations(true, [], nowSecs, nowSecs - 901), true)
  assert.equal(shouldNudgeForObservations(true, [], nowSecs, null), false)
  assert.equal(shouldNudgeForObservations(false, [], nowSecs, nowSecs - 901), false)
  assert.equal(shouldNudgeForObservations(true, { observations: [] }, nowSecs, nowSecs - 901), false)
})

test("fails closed for observations without a valid created_at", () => {
  assert.equal(shouldNudgeForObservations(true, [{}], nowSecs, nowSecs - 901), false)
  assert.equal(shouldNudgeForObservations(true, [{ created_at: null }], nowSecs, nowSecs - 901), false)
  assert.equal(shouldNudgeForObservations(true, [{ created_at: "not-a-timestamp" }], nowSecs, nowSecs - 901), false)
})

test("preserves UTC parsing and the 15-minute observation age threshold", () => {
  const createdAt = (ageSecs: number) => new Date((nowSecs - ageSecs) * 1000).toISOString()
  const naiveUTC = (ageSecs: number) => createdAt(ageSecs).replace("T", " ").replace("Z", "")
  const naiveUTCTimeSeparator = (ageSecs: number) => createdAt(ageSecs).replace("Z", "")

  assert.equal(shouldNudgeForObservations(true, [{ created_at: createdAt(899) }], nowSecs, null), false)
  assert.equal(shouldNudgeForObservations(true, [{ created_at: createdAt(900) }], nowSecs, null), true)
  assert.equal(shouldNudgeForObservations(true, [{ created_at: createdAt(901) }], nowSecs, null), true)
  assert.equal(shouldNudgeForObservations(true, [{ created_at: naiveUTC(900) }], nowSecs, null), true)
  assert.equal(shouldNudgeForObservations(true, [{ created_at: naiveUTCTimeSeparator(900) }], nowSecs, null), true)
})

// ─── Plugin lifecycle (issue #1131) ──────────────────────────────────────────

interface RecordedRequest {
  path: string
  method?: string
  body?: any
}

const PROJECT_ID = "project-1"
let runtimeImport = 0

function sessionInfo(id: string, parentID?: string, projectID = PROJECT_ID) {
  return { id, ...(parentID === undefined ? {} : { parentID }), projectID }
}

function httpResponse(data: any = { status: "created" }, ok = true) {
  return {
    ok,
    async json() {
      return data
    },
  }
}

function endRequests(requests: RecordedRequest[], sessionID: string) {
  return requests.filter(({ path, method }) => method === "POST" && path === `/sessions/${sessionID}/end`)
}

interface RuntimeOptions {
  sessionGet?: (request: { path: { id: string } }) => any
  sessionRegistration?: (sessionID: string) => any
  sessionEnd?: (sessionID: string) => any
  healthOK?: boolean | (() => boolean)
  manifestExists?: boolean
}

interface RecordedSpawn {
  command: string
  args: string[]
  options?: { cwd?: string }
}

async function createRuntime(
  t: any,
  { sessionGet, sessionRegistration, sessionEnd, healthOK = true, manifestExists = false }: RuntimeOptions = {},
) {
  const originalFetch = globalThis.fetch
  const originalEngramURL = process.env.ENGRAM_URL
  delete process.env.ENGRAM_URL
  const requests: RecordedRequest[] = []
  const registeredIDs: string[] = []
  const spawns: RecordedSpawn[] = []
  globalThis.fetch = (async (url: any, init?: any) => {
    const path = new URL(url).pathname
    if (path === "/health")
      return httpResponse(
        { status: "ok", instance_id: "00000000000000000000000000000000" },
        typeof healthOK === "function" ? healthOK() : healthOK,
      )
    const body = init?.body ? JSON.parse(init.body) : undefined
    requests.push({ path, method: init?.method, body })
    if (path === "/project/current")
      return httpResponse({ project: "engram", project_source: "git_remote" })
    if (path === "/sessions") {
      registeredIDs.push(body.id)
      return sessionRegistration ? await sessionRegistration(body.id) : httpResponse()
    }
    if (path.endsWith("/end")) {
      const sessionID = path.split("/")[2]
      return sessionEnd ? await sessionEnd(sessionID) : httpResponse({})
    }
    return httpResponse({})
  }) as typeof fetch

  t.after(() => {
    globalThis.fetch = originalFetch
    if (originalEngramURL === undefined) delete process.env.ENGRAM_URL
    else process.env.ENGRAM_URL = originalEngramURL
  })

  runtimeImport += 1
  const moduleURL = new URL(`./engram.ts?lifecycle=${runtimeImport}`, import.meta.url)
  const module = (await import(moduleURL.href)) as any
  // Issue #1218: drive the Node runtime seam directly; the host has no Bun global.
  module.nodeRuntime.spawnSync = async (command: string, args: string[]) => {
    if (args[0] === "instance-id")
      return { status: 0, stdout: Buffer.from("00000000000000000000000000000000\n") }
    return { status: 0, stdout: Buffer.from("/work/engram\n") }
  }
  module.nodeRuntime.spawn = async (command: string, args: string[], options?: { cwd?: string }) => {
    spawns.push({ command, args, options })
    return true
  }
  module.nodeRuntime.fileExists = async () => manifestExists
  const { Engram } = module
  const plugin: any = await Engram({
    directory: "/work/engram",
    project: { id: PROJECT_ID },
    client: {
      session: {
        async get(request: { path: { id: string } }) {
          if (!sessionGet) throw new Error(`unexpected SDK session.get for ${request.path.id}`)
          return sessionGet(request)
        },
      },
    },
  } as any)
  return {
    dispose: () => plugin.dispose(),
    event: (type: string, info: any) => plugin.event({ event: { type, properties: { info } } }),
    after: plugin["tool.execute.after"],
    requests,
    registeredIDs,
    spawns,
  }
}

// ─── Node runtime host (#1218) ───────────────────────────────────────────────

test("#1218 spawns serve through the Node seam on a host without a Bun global", async (t) => {
  assert.equal((globalThis as any).Bun, undefined, "the regression host must not provide a Bun global")
  let healthChecks = 0
  const runtime = await createRuntime(t, { healthOK: () => ++healthChecks > 1 })

  const serve = runtime.spawns.find(({ args }) => args[0] === "serve")
  assert.ok(serve, "the plugin must spawn `engram serve` through the Node seam")
  assert.equal(serve.command, "engram")
  assert.deepEqual(serve.args, ["serve"])

  await runtime.event("session.created", sessionInfo("sess-root"))
  assert.deepEqual(runtime.registeredIDs, ["sess-root"])
})

test("#1218 an unavailable Node seam keeps the controlled identity error instead of a Bun crash", async (t) => {
  const originalFetch = globalThis.fetch
  const originalEngramURL = process.env.ENGRAM_URL
  delete process.env.ENGRAM_URL
  globalThis.fetch = (async () => {
    throw new Error("no engram server in this scenario")
  }) as typeof fetch
  t.after(() => {
    globalThis.fetch = originalFetch
    if (originalEngramURL === undefined) delete process.env.ENGRAM_URL
    else process.env.ENGRAM_URL = originalEngramURL
  })

  runtimeImport += 1
  const moduleURL = new URL(`./engram.ts?lifecycle=${runtimeImport}`, import.meta.url)
  const { Engram, nodeRuntime } = (await import(moduleURL.href)) as any
  nodeRuntime.spawnSync = async () => null

  await assert.rejects(
    Engram({
      directory: "/work/engram",
      project: { id: PROJECT_ID },
      client: {
        session: {
          async get() {
            throw new Error("unused: the server is never reachable")
          },
        },
      },
    } as any),
    /could not resolve its local server identity/,
  )
})

test("#1218 the default Node seam resolves real Node builtins and degrades silently", async (t) => {
  runtimeImport += 1
  const moduleURL = new URL(`./engram.ts?default-seam=${runtimeImport}`, import.meta.url)
  const { nodeRuntime } = (await import(moduleURL.href)) as any

  const echo = await nodeRuntime.spawnSync(process.execPath, ["-p", "'engram-seam-echo'"])
  assert.equal(echo?.status, 0)
  assert.match(Buffer.from(echo.stdout).toString(), /engram-seam-echo/)

  const missing = await nodeRuntime.spawnSync("engram-binary-missing-for-1218", ["instance-id"])
  assert.notEqual(missing?.status, 0)

  assert.equal(await nodeRuntime.spawn(process.execPath, ["-p", "process.exit(0)"]), true)
  assert.equal(await nodeRuntime.spawn("engram-binary-missing-for-1218", ["serve"]), true)

  assert.equal(await nodeRuntime.fileExists(new URL("./engram.ts", import.meta.url).pathname), true)
  assert.equal(await nodeRuntime.fileExists("/nonexistent/engram-1218/manifest.json"), false)
})

test("#1131 ends a root registration in Engram when a later event reveals a parentID", async (t) => {
  const runtime = await createRuntime(t)
  await runtime.event("session.created", sessionInfo("sess-root"))
  await runtime.event("session.created", sessionInfo("sess-child"))
  assert.deepEqual(runtime.registeredIDs, ["sess-root", "sess-child"])

  await runtime.event("session.updated", sessionInfo("sess-child", "sess-root"))

  assert.equal(endRequests(runtime.requests, "sess-child").length, 1)

  // The session must stay de-registered: parentless events and normal hooks
  // must not register or attribute it again, and duplicate ends stay deduplicated.
  await runtime.event("session.updated", sessionInfo("sess-child", "sess-root"))
  await runtime.event("session.updated", sessionInfo("sess-child"))
  await runtime.after({ tool: "Task", sessionID: "sess-child" }, "task output long enough to be passively captured")
  assert.deepEqual(runtime.registeredIDs, ["sess-root", "sess-child"])
  assert.equal(endRequests(runtime.requests, "sess-child").length, 1)
  assert.equal(runtime.requests.filter(({ path }) => path === "/observations/passive").length, 0)
})

// A registered root whose ownership confirmation becomes invalid still owns a
// cleanup attempt, even after its known-session cache entry is removed.
async function registerMisregisteredChild(runtime: Awaited<ReturnType<typeof createRuntime>>) {
  await runtime.event("session.created", sessionInfo("sess-child"))
  await runtime.event("session.updated", { id: "sess-child", projectID: "other-project" })
}

test("#1131 retries a failed child deletion during disposal", async (t) => {
  let childEnds = 0
  const runtime = await createRuntime(t, {
    sessionEnd: (id) => httpResponse({}, id !== "sess-child" || ++childEnds > 1),
  })
  await runtime.event("session.created", sessionInfo("sess-root"))
  await registerMisregisteredChild(runtime)
  await runtime.event("session.deleted", { id: "sess-child" })
  await runtime.dispose()

  assert.equal(endRequests(runtime.requests, "sess-child").length, 2)
  assert.equal(endRequests(runtime.requests, "sess-root").length, 1)
})

test("#1131 disposal ends every known session including misregistered children", async (t) => {
  const runtime = await createRuntime(t)
  await runtime.event("session.created", sessionInfo("sess-root"))
  await registerMisregisteredChild(runtime)

  await runtime.dispose()

  assert.equal(endRequests(runtime.requests, "sess-root").length, 1)
  assert.equal(endRequests(runtime.requests, "sess-child").length, 1)
})

test("#1131 keeps deleted-root semantics: exactly one end across deletion and disposal", async (t) => {
  const runtime = await createRuntime(t)
  await runtime.event("session.created", sessionInfo("sess-root"))
  await runtime.event("session.deleted", { id: "sess-root" })
  await runtime.dispose()

  assert.equal(endRequests(runtime.requests, "sess-root").length, 1)
})

test("#1131 never ends sessions that were never registered", async (t) => {
  const runtime = await createRuntime(t)
  await runtime.event("session.created", sessionInfo("sess-root"))
  await runtime.event("session.updated", sessionInfo("sess-unknown", "sess-root"))
  await runtime.event("session.deleted", { id: "sess-unknown" })

  assert.equal(endRequests(runtime.requests, "sess-unknown").length, 0)
  assert.deepEqual(runtime.registeredIDs, ["sess-root"])
})
