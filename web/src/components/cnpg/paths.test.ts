import { describe, expect, it } from 'vitest'
import { cnpgClusterProblemsPath, cnpgDimensionPath, cnpgWithinDetail } from './paths'
import { issuesPathForSubject } from '../../utils/page-links'

describe('cnpgDimensionPath', () => {
  it('opens each health dimension where it is explained', () => {
    expect(cnpgDimensionPath('db', 'pg', 'kind', 'replication')).toBe('/cnpg/clusters/db/pg?ctx=kind&tab=replication')
    expect(cnpgDimensionPath('db', 'pg', 'kind', 'serving')).toBe('/cnpg/clusters/db/pg?ctx=kind&tab=replication')
    expect(cnpgDimensionPath('db', 'pg', 'kind', 'storage')).toBe('/cnpg/clusters/db/pg?ctx=kind&tab=storage')
    expect(cnpgDimensionPath('db', 'pg', undefined, 'protection')).toBe('/cnpg/clusters/db/pg?tab=backups')
  })
})

describe('cnpgWithinDetail', () => {
  it('turns a link to the open Cluster page into a tab change that keeps its other params', () => {
    expect(cnpgWithinDetail('/cnpg/clusters/db/pg', '?ctx=kind&tab=performance&section=history&charts=storage&drawer=x', '/cnpg/clusters/db/pg?ctx=kind&tab=backups')).toBe(
      '/cnpg/clusters/db/pg?ctx=kind&tab=backups&drawer=x',
    )
    expect(cnpgWithinDetail('/cnpg/clusters/db/pg', '?ctx=kind', '/cnpg/clusters/db/pg?ctx=kind&tab=performance&section=history&charts=replication')).toBe(
      '/cnpg/clusters/db/pg?ctx=kind&tab=performance&section=history&charts=replication',
    )
  })
  it('leaves a link to another page alone', () => {
    expect(cnpgWithinDetail('/cnpg', '', '/cnpg/clusters/db/pg?tab=replication')).toBeNull()
    expect(cnpgWithinDetail('/cnpg/clusters/db/other', '', '/cnpg/clusters/db/pg?tab=replication')).toBeNull()
  })
})

describe('cnpgClusterProblemsPath', () => {
  it('opens the Cluster with its problems listed', () => {
    expect(cnpgClusterProblemsPath('db', 'pg', 'kind')).toBe('/cnpg/clusters/db/pg?ctx=kind&problems=all')
    expect(cnpgClusterProblemsPath('db', 'pg')).toBe('/cnpg/clusters/db/pg?problems=all')
  })
})

describe('issuesPathForSubject', () => {
  it('links to the Issues page narrowed to the subject', () => {
    expect(issuesPathForSubject({ kind: 'Backup', namespace: 'pg', name: 'b-1' })).toBe('/issues?kind=Backup&resource=pg%2Fb-1')
    expect(issuesPathForSubject({ kind: 'ClusterImageCatalog', namespace: '', name: 'pg' })).toBe('/issues?kind=ClusterImageCatalog&resource=pg')
  })
  it('keeps the current namespace view filter', () => {
    expect(issuesPathForSubject({ kind: 'Cluster', namespace: 'pg', name: 'main' }, 'pg,app')).toBe('/issues?namespaces=pg%2Capp&kind=Cluster&resource=pg%2Fmain')
  })
  it('carries the API group, so a CNPG Cluster is not a CAPI one', () => {
    expect(issuesPathForSubject({ kind: 'Cluster', group: 'postgresql.cnpg.io', namespace: 'pg', name: 'main' })).toBe('/issues?kind=Cluster&group=postgresql.cnpg.io&resource=pg%2Fmain')
  })
})
