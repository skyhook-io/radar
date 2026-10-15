import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { READ_ACTIONS } from './read-actions'

const FIXTURE = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../pkg/readreason/testdata/actions.json')

describe('read actions', () => {
  it('mirror the server table exactly', () => {
    expect(READ_ACTIONS).toEqual(JSON.parse(readFileSync(FIXTURE, 'utf8')))
  })
})
