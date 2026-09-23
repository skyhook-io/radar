import { describe, expect, it } from 'vitest'
import { shouldShowWhatsNew } from './WhatsNew'
import { RELEASE_NOTES } from './releaseNotes'

const withNotes = RELEASE_NOTES[0].version

describe('shouldShowWhatsNew', () => {
  it('shows once after upgrading from a different version', () => {
    expect(shouldShowWhatsNew(withNotes, 'v1.14.1', true)).toBe(true)
  })

  it('stays closed once the version has been seen, with or without the v prefix', () => {
    expect(shouldShowWhatsNew(withNotes, withNotes, true)).toBe(false)
    expect(shouldShowWhatsNew(withNotes.slice(1), withNotes, true)).toBe(false)
  })

  it('stays closed on a fresh install', () => {
    expect(shouldShowWhatsNew(withNotes, null, false)).toBe(false)
  })

  it('shows for installs that predate the seen marker', () => {
    expect(shouldShowWhatsNew(withNotes, null, true)).toBe(true)
  })

  it('stays closed for versions without release notes', () => {
    expect(shouldShowWhatsNew('v0.0.1', 'v0.0.0', true)).toBe(false)
    expect(shouldShowWhatsNew('dev', 'v1.14.1', true)).toBe(false)
  })
})
