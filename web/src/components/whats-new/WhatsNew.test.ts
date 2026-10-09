import { describe, expect, it } from 'vitest'
import { Megaphone } from 'lucide-react'
import { nextSeenVersion, whatsNewToOpen } from './WhatsNew'
import {
  AUTO_OPEN_SCORE,
  composeReleaseNotes,
  latestReleaseNotesFor,
  MAX_CARDS,
  MAX_LINES,
  releaseLine,
  RELEASE_NOTES,
  releaseNotesFor,
  unreadReleases,
  type ReleaseNotes,
} from './releaseNotes'
import { compareVersions } from '../../utils/version'

const highlight = (id: string, importance: number) => ({ id, icon: Megaphone, title: id, description: 'd', importance })

const entry = (version: string, importances: number[] = [9, 9, 9], improvements: string[] = []): ReleaseNotes => ({
  version,
  highlights: importances.map((importance, i) => highlight(`${version}-${i}`, importance)),
  improvements,
})

const catalog = [entry('v1.15.0'), entry('v2.0.0')]

const unread = (current: string, lastSeen: string | null, prior = true, list = catalog) =>
  unreadReleases(current, lastSeen, prior, list).map(n => n.version)

describe('unreadReleases', () => {
  it('lists the releases after the last one seen, newest first', () => {
    expect(unread('v2.0.0', 'v1.14.0')).toEqual(['v2.0.0', 'v1.15.0'])
    expect(unread('v2.0.0', 'v1.15.0')).toEqual(['v2.0.0'])
    expect(unread('v2.0.0', 'v2.0.0')).toEqual([])
    expect(unread('2.0.0', 'v2.0.0')).toEqual([])
  })

  it('covers a patch of a release with notes, and nothing above the running version', () => {
    expect(unread('v1.15.1', 'v1.14.1')).toEqual(['v1.15.0'])
    expect(unread('v2.3.4', 'v1.15.2')).toEqual(['v2.0.0'])
  })

  it('is empty once a later release was seen, including after a downgrade', () => {
    expect(unread('v1.15.2', 'v1.15.1')).toEqual([])
    expect(unread('v1.15.0', 'v2.0.0')).toEqual([])
  })

  it('is empty on a fresh install, and covers everything for installs that predate the seen record', () => {
    expect(unread('v2.0.0', null, false)).toEqual([])
    expect(unread('v2.0.0', null, true)).toEqual(['v2.0.0', 'v1.15.0'])
  })

  it('treats a record that is not a version as an install with nothing seen', () => {
    expect(unread('v2.0.0', 'vdev', false)).toEqual(['v2.0.0', 'v1.15.0'])
  })

  it('is empty when no notes exist at or below the running version', () => {
    expect(unread('v1.14.9', 'v1.14.0')).toEqual([])
    expect(unread('dev', 'v1.14.1')).toEqual([])
  })

  it('orders by version whatever the catalog order', () => {
    expect(unread('v2.0.0', null, true, [entry('v1.15.0'), entry('v2.0.0'), entry('v1.16.0')])).toEqual(['v2.0.0', 'v1.16.0', 'v1.15.0'])
  })
})

describe('composeReleaseNotes', () => {
  it('ranks highlights by importance, weighting older releases down', () => {
    const notes = composeReleaseNotes([entry('v1.17.0', [6]), entry('v1.16.0', [9]), entry('v1.15.0', [10])])
    expect(notes.highlights.map(h => [h.id, h.score])).toEqual([
      ['v1.16.0-0', 6.75],
      ['v1.17.0-0', 6],
      ['v1.15.0-0', 5],
    ])
  })

  it('breaks ties toward the newer release, then the authored order', () => {
    const notes = composeReleaseNotes([entry('v1.16.0', [3, 6]), entry('v1.15.0', [8])])
    expect(notes.highlights.map(h => h.id)).toEqual(['v1.16.0-1', 'v1.15.0-0', 'v1.16.0-0'])
  })

  it('keeps the authored order of a single release with equal importance', () => {
    expect(composeReleaseNotes([entry('v1.16.0', [5, 5, 5])]).highlights.map(h => h.id)).toEqual(['v1.16.0-0', 'v1.16.0-1', 'v1.16.0-2'])
  })

  it('shows at most three releases and says when older ones were left out', () => {
    const four = [entry('v1.18.0'), entry('v1.17.0'), entry('v1.16.0'), entry('v1.15.0')]
    const notes = composeReleaseNotes(four)
    expect(notes.versions).toEqual(['v1.18.0', 'v1.17.0', 'v1.16.0'])
    expect(notes.complete).toBe(false)
    expect(notes.highlights.some(h => h.version === 'v1.15.0')).toBe(false)
    expect(composeReleaseNotes(four.slice(0, 3)).complete).toBe(true)
  })

  it('turns highlights past the cards into lines ahead of the improvements, within the line cap', () => {
    const notes = composeReleaseNotes([
      entry('v1.16.0', [7, 7, 7, 7, 7, 2], ['a', 'b', 'c', 'd', 'e', 'f', 'g', 'h']),
      entry('v1.15.0', [1], ['old']),
    ])
    expect(notes.highlights).toHaveLength(MAX_CARDS)
    expect(notes.lines).toHaveLength(MAX_LINES)
    expect(notes.lines.slice(0, 2).map(l => l.text)).toEqual(['v1.16.0-5', 'v1.15.0-0'])
    expect(notes.lines.map(l => l.text)).not.toContain('old')
  })

  it('scores the dialog by the cards it shows', () => {
    expect(composeReleaseNotes([entry('v1.16.0', [4, 4, 4, 4, 4, 10])]).score).toBe(10 + 4 * 4)
  })
})

describe('whatsNewToOpen', () => {
  it('opens when the unseen releases show enough, and not for a quiet release', () => {
    const list = [entry('v1.15.0', [9, 8, 7, 7, 7]), entry('v1.16.0', [6, 5, 4])]
    expect(whatsNewToOpen('v1.15.0', 'v1.14.1', true, list)?.score).toBe(38)
    expect(whatsNewToOpen('v1.16.0', 'v1.15.0', true, list)).toBeNull()
    expect(whatsNewToOpen('v1.16.0', 'v1.14.1', true, list)?.score).toBeGreaterThanOrEqual(AUTO_OPEN_SCORE)
  })

  it('stays closed when nothing is unseen', () => {
    expect(whatsNewToOpen('v2.0.0', 'v2.0.0', true, catalog)).toBeNull()
    expect(whatsNewToOpen('v2.0.0', null, false, catalog)).toBeNull()
  })
})

describe('nextSeenVersion', () => {
  it('only moves forward', () => {
    expect(nextSeenVersion('v2.0.0', 'v1.15.0')).toBe('v2.0.0')
    expect(nextSeenVersion('2.0.1', null)).toBe('v2.0.1')
    expect(nextSeenVersion('1.15.0', 'v2.0.0')).toBeNull()
    expect(nextSeenVersion('v2.0.0', 'v2.0.0')).toBeNull()
  })

  it('never records a development build, and replaces a record that is not a version', () => {
    expect(nextSeenVersion('dev', null)).toBeNull()
    expect(nextSeenVersion('dev', 'v2.0.0')).toBeNull()
    expect(nextSeenVersion('v2.0.0', 'vdev')).toBe('v2.0.0')
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

describe('the shipped catalog', () => {
  it('has one well-formed entry per release', () => {
    const versions = RELEASE_NOTES.map(n => n.version)
    expect(new Set(versions).size).toBe(versions.length)
    for (const notes of RELEASE_NOTES) {
      expect(notes.version, notes.version).toMatch(/^v\d+\.\d+\.\d+$/)
      expect(notes.highlights.length, `${notes.version} needs a highlight`).toBeGreaterThan(0)
      expect(new Set(notes.highlights.map(h => h.id)).size, `${notes.version} highlight ids`).toBe(notes.highlights.length)
      expect(new Set(notes.improvements).size, `${notes.version} improvements`).toBe(notes.improvements.length)
      const top = notes.highlights.reduce((best, h) => (h.importance > best.importance ? h : best))
      for (const h of notes.highlights) {
        expect(h.description.length, `${notes.version}/${h.id}: description fits a card`).toBeLessThanOrEqual(150)
        if (h.leadDescription) expect(h.id, `${notes.version}/${h.id}: only the release's top highlight can lead`).toBe(top.id)
        expect(Number.isInteger(h.importance) && h.importance >= 1 && h.importance <= 10, `${notes.version}/${h.id}: importance is 1-10`).toBe(true)
        expect(!!h.path === !!h.cta, `${notes.version}/${h.id}: path and cta go together`).toBe(true)
        if (h.path) expect(h.path, `${notes.version}/${h.id}`).toMatch(/^\//)
      }
    }
  })
})

describe('releaseLine', () => {
  it('names the minor line, since its patches show the same notes', () => {
    expect(releaseLine('v1.15.0')).toBe('v1.15')
    expect(releaseLine('v2.0.0')).toBe('v2.0')
  })
})
