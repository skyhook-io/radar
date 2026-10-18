import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { canonicalReadCode, READ_ACTIONS, readActionFor } from './read-actions'

const FIXTURE = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../pkg/readreason/testdata/actions.json')

describe('read actions', () => {
  it('mirror the server table exactly', () => {
    expect(READ_ACTIONS).toEqual(JSON.parse(readFileSync(FIXTURE, 'utf8')))
  })
  it('read the spellings older Radars sent as the codes they became', () => {
    expect(canonicalReadCode('kind_sync_failed')).toBe('kind_sync_timed_out')
    expect(canonicalReadCode('rbac_denied')).toBe('user_forbidden')
    expect(readActionFor('kind_sync_failed')).toBe('wait')
    expect(readActionFor('rbac_denied')).toBe('user_access')
  })
})
