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
