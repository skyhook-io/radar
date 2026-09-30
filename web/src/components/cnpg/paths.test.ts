import { describe, expect, it } from 'vitest'
import { cnpgDimensionPath } from './paths'

describe('cnpgDimensionPath', () => {
  it('opens each health dimension where it is explained', () => {
    expect(cnpgDimensionPath('db', 'pg', 'kind', 'replication')).toBe('/cnpg/clusters/db/pg?ctx=kind&tab=runtime')
    expect(cnpgDimensionPath('db', 'pg', 'kind', 'serving')).toBe('/cnpg/clusters/db/pg?ctx=kind&tab=runtime')
    expect(cnpgDimensionPath('db', 'pg', 'kind', 'storage')).toBe('/cnpg/clusters/db/pg?ctx=kind&tab=runtime&section=storage')
    expect(cnpgDimensionPath('db', 'pg', undefined, 'protection')).toBe('/cnpg/clusters/db/pg?tab=protection')
  })
})
