import { describe, it, expect } from 'vitest'
import { decideReveal } from './secret-reveal'

// SKY-830 bug 37: revealing a Secret value used to flip immediately,
// with the warning banner below the data section (so the user only saw
// the warning AFTER the value was already on screen). The fix gates
// the FIRST reveal in a panel mount behind a confirmation dialog;
// subsequent reveals don't re-prompt (re-prompting per-key would be
// hostile for secrets with many keys). These tests pin that rule.

describe('decideReveal', () => {
  it('returns "hide" when the key is currently revealed (Hide never re-confirms)', () => {
    const state = { revealed: new Set(['tls.crt']), hasConfirmedReveal: false }
    expect(decideReveal(state, 'tls.crt')).toBe('hide')
  })

  it('returns "hide" even when revealed AND already confirmed', () => {
    const state = { revealed: new Set(['tls.crt']), hasConfirmedReveal: true }
    expect(decideReveal(state, 'tls.crt')).toBe('hide')
  })

  it('returns "prompt" on the first reveal of any key in the mount', () => {
    const state = { revealed: new Set<string>(), hasConfirmedReveal: false }
    expect(decideReveal(state, 'tls.crt')).toBe('prompt')
    expect(decideReveal(state, 'tls.key')).toBe('prompt')
    expect(decideReveal(state, 'token')).toBe('prompt')
  })

  it('returns "reveal" once the user has confirmed at least once', () => {
    const state = { revealed: new Set(['tls.crt']), hasConfirmedReveal: true }
    // Revealing a different (not-yet-revealed) key after confirmation
    // should go through immediately, no second prompt.
    expect(decideReveal(state, 'tls.key')).toBe('reveal')
    expect(decideReveal(state, 'token')).toBe('reveal')
  })

  it('does not re-prompt when toggling between Hide and Reveal on the same key after confirmation', () => {
    // Step 1: confirm and reveal "tls.crt".
    let state: { revealed: Set<string>; hasConfirmedReveal: boolean } = {
      revealed: new Set(['tls.crt']),
      hasConfirmedReveal: true,
    }
    // Step 2: hide it.
    expect(decideReveal(state, 'tls.crt')).toBe('hide')
    state = { revealed: new Set(), hasConfirmedReveal: true }
    // Step 3: reveal again — should NOT prompt.
    expect(decideReveal(state, 'tls.crt')).toBe('reveal')
  })
})
