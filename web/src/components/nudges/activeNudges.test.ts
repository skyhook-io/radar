import { afterEach, describe, expect, it } from 'vitest'
import { otherNudgeVisible, resetNudgesForTest, setNudgeVisible } from './activeNudges'

afterEach(resetNudgesForTest)

describe('active nudges', () => {
  it('reports other nudges but not the caller', () => {
    setNudgeVisible('oss-poll', true)
    expect(otherNudgeVisible('oss-poll')).toBe(false)
    setNudgeVisible('update-notice', true)
    expect(otherNudgeVisible('oss-poll')).toBe(true)
    setNudgeVisible('update-notice', false)
    expect(otherNudgeVisible('oss-poll')).toBe(false)
  })
})
