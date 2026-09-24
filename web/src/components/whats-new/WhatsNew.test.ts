import { describe, expect, it } from 'vitest'
import { Megaphone } from 'lucide-react'
import { shouldShowWhatsNew } from './WhatsNew'
import { releaseNotesFor, type ReleaseNotes } from './releaseNotes'

const catalog: ReleaseNotes[] = [{
  version: 'v2.0.0',
  releaseUrl: 'https://github.com/skyhook-io/radar/releases',
  highlights: [{ id: 'x', icon: Megaphone, title: 'X', description: 'Y' }],
  improvements: [],
}]

const show = (current: string, lastSeen: string | null, prior: boolean) =>
  shouldShowWhatsNew(current, lastSeen, prior, catalog)

describe('shouldShowWhatsNew', () => {
  it('shows once after upgrading from a different version', () => {
    expect(show('v2.0.0', 'v1.14.1', true)).toBe(true)
  })

  it('stays closed once the version has been seen, with or without the v prefix', () => {
    expect(show('v2.0.0', 'v2.0.0', true)).toBe(false)
    expect(show('2.0.0', 'v2.0.0', true)).toBe(false)
  })

  it('stays closed on a fresh install', () => {
    expect(show('v2.0.0', null, false)).toBe(false)
  })

  it('shows for installs that predate the seen marker', () => {
    expect(show('v2.0.0', null, true)).toBe(true)
  })

  it('stays closed for versions without release notes', () => {
    expect(show('v0.0.1', 'v0.0.0', true)).toBe(false)
    expect(show('dev', 'v1.14.1', true)).toBe(false)
  })
})

describe('releaseNotesFor', () => {
  it('matches versions with or without the v prefix', () => {
    expect(releaseNotesFor('2.0.0', catalog)?.version).toBe('v2.0.0')
    expect(releaseNotesFor('v2.0.0', catalog)?.version).toBe('v2.0.0')
    expect(releaseNotesFor(undefined, catalog)).toBeUndefined()
  })
})
