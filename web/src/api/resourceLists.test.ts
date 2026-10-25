import { describe, expect, it } from 'vitest'
import type { ResourceReadCoverage } from '@skyhook-io/k8s-ui/types/resource-coverage'
import { combinedListStatus, createListRefetchInterval, listSyncPending, normalizeResourceList } from './resourceLists'

const complete: ResourceReadCoverage = { status: 'complete', gaps: [], scope: { type: 'cluster' }, read: { type: 'cluster' } }
const partial: ResourceReadCoverage = { ...complete, status: 'partial', gaps: [{ code: 'radar_namespace_not_collected' }] }
const columns = [{ name: 'State', type: 'string' }]

describe('resource-list responses, read by shape', () => {
  it('reads an older Radar array as rows of unknown coverage', () => {
    const rows = [{ metadata: { name: 'api' } }]
    expect(normalizeResourceList(rows)).toEqual({ items: rows, coverage: { status: 'unknown' }, printerTable: null })
    expect(normalizeResourceList([]).coverage).toEqual({ status: 'unknown' })
  })
  it('reads an older Radar table envelope as a printer table of unknown coverage', () => {
    const body = { items: [{ metadata: { uid: 'one' } }], kind: 'widgets', group: 'example.io', columns, cells: { one: ['Ready'] } }
    const result = normalizeResourceList(body)
    expect(result.items).toEqual(body.items)
    expect(result.coverage).toEqual({ status: 'unknown' })
    expect(result.printerTable?.cells).toEqual(body.cells)
  })
  it('reads the coverage envelope without a table, retaining partial metadata with zero rows', () => {
    expect(normalizeResourceList({ items: [], coverage: partial, kind: 'pods', columns: null, cells: null })).toEqual({ items: [], coverage: partial, printerTable: null })
  })
  it('reads the coverage envelope with a table, keeping rows and cells from the same response', () => {
    const body = { items: [{ metadata: { uid: 'one' } }], kind: 'widgets', group: 'example.io', columns, cells: { one: ['Ready'] }, coverage: complete }
    const result = normalizeResourceList(body)
    expect(result.items).toEqual(body.items)
    expect(result.coverage).toEqual(complete)
    expect(result.printerTable?.cells).toEqual(body.cells)
  })
})

describe('listSyncPending', () => {
  it('holds only while a partial answer has namespaces still loading', () => {
    expect(listSyncPending({ ...partial, gaps: [{ code: 'radar_forbidden' }, { code: 'kind_sync_pending' }] })).toBe(true)
    expect(listSyncPending(partial)).toBe(false)
    expect(listSyncPending(complete)).toBe(false)
    expect(listSyncPending({ status: 'unknown' })).toBe(false)
    expect(listSyncPending(undefined)).toBe(false)
  })
})

describe('combinedListStatus', () => {
  it('is partial when any read failed or was partial', () => {
    expect(combinedListStatus([complete], true, false)).toBe('partial')
    expect(combinedListStatus([complete, partial], false, false)).toBe('partial')
  })
  it("is partial when Radar's collection narrowed a read, since the view shows cluster totals", () => {
    const narrowed = { ...complete, narrowedBy: ['namespace_collection'] }
    expect(combinedListStatus([complete, narrowed], false, false)).toBe('partial')
    expect(combinedListStatus([{ ...complete, narrowedBy: ['user_access'] }], false, false)).toBe('complete')
  })
  it('is complete only when every read was', () => {
    expect(combinedListStatus([complete, complete], false, false)).toBe('complete')
    expect(combinedListStatus([complete, { status: 'unknown' }], false, false)).toBe('unknown')
  })
  it('treats discovery finding nothing to read as complete, and no reads otherwise as unknown', () => {
    expect(combinedListStatus([], false, true)).toBe('complete')
    expect(combinedListStatus([], false, false)).toBe('unknown')
  })
})

 describe('loading list backoff', () => {
  it('backs off per response, caps, and resets for coverage or query changes', () => {
   const interval = createListRefetchInterval(120000)
   const pending = { ...partial, gaps: [{ code: 'kind_sync_pending' }] }
   const query = { state: { data: { coverage: pending }, dataUpdateCount: 1 } }
   for (const delay of [5000, 10000, 20000, 30000, 30000]) {
    expect(interval(query)).toBe(delay)
    expect(interval(query)).toBe(delay)
    query.state.dataUpdateCount++
   }
   query.state.data.coverage = complete
   expect(interval(query)).toBe(120000)
   query.state.data.coverage = pending
   expect(interval(query)).toBe(5000)
   query.state.dataUpdateCount++
   expect(interval(query)).toBe(10000)
   const other = { state: { ...query.state } }
   expect(interval(other)).toBe(5000)
   expect(interval(query)).toBe(5000)
  })
 })
