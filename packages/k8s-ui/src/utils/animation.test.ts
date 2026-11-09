import { describe, expect, it } from 'vitest'
import {
  DURATION_HOVER_REVEAL_IN,
  DURATION_HOVER_REVEAL_OUT,
  HOVER_INTENT_DELAY,
  HOVER_REVEAL_ACTION,
  HOVER_REVEAL_HIT,
  HOVER_REVEAL_REST,
} from './animation'

// The presets spell their timing out for Tailwind's scanner; this keeps the
// literals in step with the named constants.
describe('hover-reveal presets', () => {
  const intent = `delay-${HOVER_INTENT_DELAY}`
  const into = `duration-${DURATION_HOVER_REVEAL_IN}`
  const outOf = `duration-${DURATION_HOVER_REVEAL_OUT}`

  it.each([
    ['action', HOVER_REVEAL_ACTION],
    ['hit target', HOVER_REVEAL_HIT],
  ])('the %s leaves on the out clock and comes in on the in clock after the intent delay', (_, preset) => {
    const classes = preset.split(/\s+/)
    expect(classes).toContain(outOf)
    for (const v of ['group-hover', 'group-has-[:focus-visible]']) {
      expect(classes).toContain(`${v}:${into}`)
      expect(classes).toContain(`${v}:${intent}`)
    }
  })

  it('the resting content steps out on the out clock and returns on the in clock', () => {
    const classes = HOVER_REVEAL_REST.split(/\s+/)
    expect(classes).toContain(into)
    for (const v of ['group-hover', 'group-has-[:focus-visible]']) {
      expect(classes).toContain(`${v}:${outOf}`)
      expect(classes).toContain(`${v}:${intent}`)
    }
  })
})
