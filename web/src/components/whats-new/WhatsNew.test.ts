import { describe, expect, it } from 'vitest'
import { Megaphone } from 'lucide-react'
import { nextSeenVersion, whatsNewToShow } from './WhatsNew'
import { latestReleaseNotesFor, releaseNotesFor, type ReleaseNotes } from './releaseNotes'
import { compareVersions } from '../../utils/version'

const entry = (version: string): ReleaseNotes => ({
  version,
  releaseUrl: 'https://github.com/skyhook-io/radar/releases',
  highlights: [{ id: 'x', icon: Megaphone, title: 'X', description: 'Y' }],
  improvements: [],
})

const catalog = [entry('v1.15.0'), entry('v2.0.0')]

const show = (current: string, lastSeen: string | null, prior = true) =>
  whatsNewToShow(current, lastSeen, prior, catalog)?.version ?? null

describe('whatsNewToShow', () => {
  it('shows once after upgrading to a release with notes', () => {
    expect(show('v2.0.0', 'v1.15.0')).toBe('v2.0.0')
    expect(show('v2.0.0', 'v2.0.0')).toBeNull()
    expect(show('2.0.0', 'v2.0.0')).toBeNull()
  })

  it('shows the newest earlier notes when the running release has none of its own', () => {
    expect(show('v1.15.1', 'v1.14.1')).toBe('v1.15.0')
    expect(show('v2.3.4', 'v1.15.2')).toBe('v2.0.0')
  })

  it('stays closed once a later release was seen, including after a downgrade', () => {
    expect(show('v1.15.2', 'v1.15.1')).toBeNull()
    expect(show('v1.15.0', 'v2.0.0')).toBeNull()
  })

  it('stays closed on a fresh install, and shows for installs that predate the seen record', () => {
    expect(show('v2.0.0', null, false)).toBeNull()
    expect(show('v2.0.0', null, true)).toBe('v2.0.0')
  })

  it('stays closed when no notes exist at or below the running version', () => {
    expect(show('v1.14.9', 'v1.14.0')).toBeNull()
    expect(show('dev', 'v1.14.1')).toBeNull()
  })
})

describe('nextSeenVersion', () => {
  it('only moves forward', () => {
    expect(nextSeenVersion('v2.0.0', 'v1.15.0')).toBe('v2.0.0')
    expect(nextSeenVersion('1.15.0', 'v2.0.0')).toBe('v2.0.0')
    expect(nextSeenVersion('2.0.1', null)).toBe('v2.0.1')
    expect(nextSeenVersion('dev', 'v2.0.0')).toBe('v2.0.0')
  })
})

describe('release notes lookup', () => {
  it('matches an exact version with or without the v prefix', () => {
    expect(releaseNotesFor('2.0.0', catalog)?.version).toBe('v2.0.0')
    expect(releaseNotesFor(undefined, catalog)).toBeUndefined()
  })

  it('picks the newest entry at or below a version, whatever the catalog order', () => {
    expect(latestReleaseNotesFor('v2.1.0', [entry('v2.0.0'), entry('v1.15.0')])?.version).toBe('v2.0.0')
    expect(latestReleaseNotesFor('v1.99.0', catalog)?.version).toBe('v1.15.0')
    expect(latestReleaseNotesFor('v1.0.0', catalog)).toBeUndefined()
  })
})

describe('compareVersions', () => {
  it('orders by semver, with a prerelease before its release', () => {
    expect(compareVersions('v1.10.0', 'v1.9.9')).toBeGreaterThan(0)
    expect(compareVersions('1.2.3', 'v1.2.3')).toBe(0)
    expect(compareVersions('v1.2.3-rc.1', 'v1.2.3')).toBeLessThan(0)
    expect(compareVersions('dev', 'v1.2.3')).toBeNull()
  })
})
