import { describe, expect, it } from 'vitest'
import { cnpgDimensionPath, cnpgWithinDetail } from './paths'

describe('cnpgDimensionPath', () => {
  it('opens each health dimension where it is explained', () => {
    expect(cnpgDimensionPath('db', 'pg', 'kind', 'replication')).toBe('/cnpg/clusters/db/pg?ctx=kind&tab=runtime')
    expect(cnpgDimensionPath('db', 'pg', 'kind', 'serving')).toBe('/cnpg/clusters/db/pg?ctx=kind&tab=runtime')
    expect(cnpgDimensionPath('db', 'pg', 'kind', 'storage')).toBe('/cnpg/clusters/db/pg?ctx=kind&tab=runtime&section=storage')
    expect(cnpgDimensionPath('db', 'pg', undefined, 'protection')).toBe('/cnpg/clusters/db/pg?tab=protection')
  })
})

describe('cnpgWithinDetail', () => {
  it('turns a link to the open Cluster page into a tab change that keeps its other params', () => {
    expect(cnpgWithinDetail('/cnpg/clusters/db/pg', '?ctx=kind&tab=runtime&section=sessions&drawer=x', '/cnpg/clusters/db/pg?ctx=kind&tab=protection')).toBe(
      '/cnpg/clusters/db/pg?ctx=kind&tab=protection&drawer=x',
    )
    expect(cnpgWithinDetail('/cnpg/clusters/db/pg', '?ctx=kind', '/cnpg/clusters/db/pg?ctx=kind&tab=runtime&section=storage')).toBe(
      '/cnpg/clusters/db/pg?ctx=kind&tab=runtime&section=storage',
    )
  })
  it('leaves a link to another page alone', () => {
    expect(cnpgWithinDetail('/cnpg', '', '/cnpg/clusters/db/pg?tab=runtime')).toBeNull()
    expect(cnpgWithinDetail('/cnpg/clusters/db/other', '', '/cnpg/clusters/db/pg?tab=runtime')).toBeNull()
  })
})
