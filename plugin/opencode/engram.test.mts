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

async function createRuntime(t: any, { sessionGet }: { sessionGet?: (request: { path: { id: string } }) => any } = {}) {
  const originalFetch = globalThis.fetch
  const originalBun = (globalThis as any).Bun
  const originalEngramURL = process.env.ENGRAM_URL
  delete process.env.ENGRAM_URL
  const requests: RecordedRequest[] = []
  const registeredIDs: string[] = []
  const bun = globalThis as any
  bun.Bun = {
    spawnSync(args: string[]) {
      if (args[1] === "instance-id")
        return { exitCode: 0, stdout: Buffer.from("00000000000000000000000000000000\n") }
      return { exitCode: 0, stdout: Buffer.from("/work/engram\n") }
    },
    spawn() {},
    file() {
      return {
        async exists() {
          return false
        },
      }
    },
  }
  globalThis.fetch = (async (url: any, init?: any) => {
    const path = new URL(url).pathname
    if (path === "/health")
      return httpResponse({ status: "ok", instance_id: "00000000000000000000000000000000" })
    const body = init?.body ? JSON.parse(init.body) : undefined
    requests.push({ path, method: init?.method, body })
    if (path === "/project/current")
      return httpResponse({ project: "engram", project_source: "git_remote" })
    if (path === "/sessions") {
      registeredIDs.push(body.id)
      return httpResponse()
    }
    return httpResponse({})
  }) as typeof fetch

  t.after(() => {
    globalThis.fetch = originalFetch
    bun.Bun = originalBun
    if (originalEngramURL === undefined) delete process.env.ENGRAM_URL
    else process.env.ENGRAM_URL = originalEngramURL
  })

  runtimeImport += 1
  const moduleURL = new URL(`./engram.ts?lifecycle=${runtimeImport}`, import.meta.url)
  const { Engram } = await import(moduleURL.href)
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
  }
}

test("#1131 ends a root registration in Engram when a later event reveals a parentID", async (t) => {
  const runtime = await createRuntime(t)
  await runtime.event("session.created", sessionInfo("sess-root"))
  await runtime.event("session.created", sessionInfo("sess-child"))
  assert.deepEqual(runtime.registeredIDs, ["sess-root", "sess-child"])

  await runtime.event("session.updated", sessionInfo("sess-child", "sess-root"))

  assert.equal(endRequests(runtime.requests, "sess-child").length, 1)

  // The session must stay de-registered: a later root-path flow must not
  // register it again and duplicate events must not end it twice.
  await runtime.event("session.updated", sessionInfo("sess-child", "sess-root"))
  await runtime.after({ tool: "Bash", sessionID: "sess-child" }, { args: {} })
  assert.deepEqual(runtime.registeredIDs, ["sess-root", "sess-child"])
  assert.equal(endRequests(runtime.requests, "sess-child").length, 1)
})

// A registered root whose ownership confirmation was invalidated by a
// later malformed event stays in knownSessions but is no longer an
// authoritative root: the legacy misregistration that must still be ended.
async function registerMisregisteredChild(runtime: Awaited<ReturnType<typeof createRuntime>>) {
  await runtime.event("session.created", sessionInfo("sess-child"))
  await runtime.event("session.updated", { id: "sess-child", projectID: "other-project" })
}

test("#1131 ends a known child registration when session.deleted fires", async (t) => {
  const runtime = await createRuntime(t)
  await runtime.event("session.created", sessionInfo("sess-root"))
  await registerMisregisteredChild(runtime)

  await runtime.event("session.deleted", { id: "sess-child" })

  assert.equal(endRequests(runtime.requests, "sess-child").length, 1)
  assert.equal(endRequests(runtime.requests, "sess-root").length, 0)
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

test("#1131 duplicate reclassification events fire exactly one end POST", async (t) => {
  const runtime = await createRuntime(t)
  await runtime.event("session.created", sessionInfo("sess-child"))
  await runtime.event("session.updated", sessionInfo("sess-child", "sess-root"))
  await runtime.event("session.updated", sessionInfo("sess-child", "sess-root"))
  await runtime.dispose()

  assert.equal(endRequests(runtime.requests, "sess-child").length, 1)
})
