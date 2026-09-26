import { spawnSync } from 'node:child_process'
import { mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

// scripts/check-whats-new.sh gates every minor release on having notes; these
// pin what counts as an entry, against catalogs written to a temp dir.
const script = join(__dirname, '..', '..', '..', '..', 'scripts', 'check-whats-new.sh')

function check(version: string, catalog: string) {
  const file = join(mkdtempSync(join(tmpdir(), 'whats-new-')), 'releaseNotes.ts')
  writeFileSync(file, catalog)
  return spawnSync('bash', [script, version], { env: { ...process.env, WHATS_NEW_CATALOG: file }, encoding: 'utf8' }).status
}

const empty = `export const RELEASE_NOTES: ReleaseNotes[] = []\n`
const withEntry = (entry: string, before = '') => `${before}export const RELEASE_NOTES: ReleaseNotes[] = [\n${entry}\n]\n\nexport function after() {}\n`

describe('check-whats-new.sh', () => {
  it('lets patches and prereleases through without notes', () => {
    expect(check('v1.15.1', empty)).toBe(0)
    expect(check('v1.15.0-rc.1', empty)).toBe(0)
  })

  it('refuses a minor or major release without notes, including one with build metadata', () => {
    expect(check('v1.15.0', empty)).toBe(1)
    expect(check('v2.0.0', empty)).toBe(1)
    expect(check('v1.15.0+build.1', empty)).toBe(1)
  })

  it('refuses a version that is not vX.Y.Z', () => {
    expect(check('v1.15', empty)).toBe(1)
    expect(check('latest', empty)).toBe(1)
  })

  it('accepts a real entry, wherever version sits in it', () => {
    const entry = `  {\n    releaseUrl: 'https://github.com/skyhook-io/radar/releases/tag/v1.15.0', version: 'v1.15.0',\n    highlights: [],\n  },`
    expect(check('v1.15.0', withEntry(entry))).toBe(0)
    expect(check('1.15.0+build.1', withEntry(entry))).toBe(0)
  })

  it('does not count comments, objects outside the catalog, or look-alike properties', () => {
    const decoys = `// { version: 'v1.16.0' },\n/* { version: 'v1.17.0' } */\nconst draft = { version: 'v1.18.0' }\n`
    const catalog = withEntry(`  // { version: 'v1.20.0' },\n  { notversion: 'v1.19.0' },`, decoys)
    for (const v of ['v1.16.0', 'v1.17.0', 'v1.18.0', 'v1.19.0', 'v1.20.0']) expect(check(v, catalog), v).toBe(1)
  })

  it('ignores an object after an empty catalog, even with a semicolon', () => {
    expect(check('v1.15.0', `export const RELEASE_NOTES: ReleaseNotes[] = [];\nconst draft = { version: 'v1.15.0' };\n`)).toBe(1)
  })

  it('does not count the words "version:" inside another entry\'s text', () => {
    const entry = `  {\n    version: 'v1.14.0',\n    improvements: ["Set version: 'v1.15.0' in your configuration"],\n  },`
    expect(check('v1.15.0', withEntry(entry))).toBe(1)
  })

  it('accepts a value on the line after its key, quoted keys, and space before the colon', () => {
    expect(check('v1.15.0', withEntry(`  {\n    version:\n      'v1.15.0',\n  },`))).toBe(0)
    expect(check('v1.15.0', withEntry(`  { version: "v1.15.0" },`))).toBe(0)
    expect(check('v1.15.0', withEntry(`  { "version": "v1.15.0" },`))).toBe(0)
    expect(check('v1.15.0', withEntry(`  { version : 'v1.15.0' },`))).toBe(0)
    expect(check('v1.15.0', withEntry(`  {\n    "version" :\n      "v1.15.0",\n  },`))).toBe(0)
  })
})
