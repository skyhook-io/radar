// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import type { WorkloadHistoryPage } from '../../api/client'
import type { TimelineEvent } from '../../types'
import { useHistoryPaging } from './historyPaging'

const page = (prefix: string, from: number, to: number, truncated: boolean): WorkloadHistoryPage => ({
  events: Array.from({ length: from - to + 1 }, (_, i) => ({ id: `${prefix}${from - i}` }) as TimelineEvent),
  truncated,
  nextBeforeSeq: truncated ? to : undefined,
})

type Paging = ReturnType<typeof useHistoryPaging>
const renders: Paging[] = []
function Harness(props: { identity: string; newest?: WorkloadHistoryPage; fetchOlder: (seq: number) => Promise<WorkloadHistoryPage> }) {
  renders.push(useHistoryPaging(props.identity, props.newest, props.fetchOlder))
  return null
}
const latest = () => renders[renders.length - 1]

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let root: Root
let element: HTMLDivElement
beforeEach(() => {
  renders.length = 0
  element = document.createElement('div')
  root = createRoot(element)
})
afterEach(async () => {
  await act(async () => root.unmount())
})
const render = (props: Parameters<typeof Harness>[0]) => act(async () => root.render(<Harness {...props} />))
const ids = () => (latest().events ?? []).map((e) => e.id).sort()
function deferred() {
  let resolve!: (p: WorkloadHistoryPage) => void
  const promise = new Promise<WorkloadHistoryPage>((r) => (resolve = r))
  return { promise, resolve }
}

describe('useHistoryPaging', () => {
  it("shows only the new workload's history after navigating away from loaded older pages", async () => {
    const fetchOlder = async () => page('a', 2, 1, false)
    await render({ identity: 'A', newest: page('a', 4, 3, true), fetchOlder })
    await act(async () => latest().loadOlder())
    expect(ids()).toEqual(['a1', 'a2', 'a3', 'a4'])
    await render({ identity: 'B', newest: page('b', 2, 1, false), fetchOlder })
    expect(ids()).toEqual(['b1', 'b2'])
    expect(latest().truncated).toBe(false)
  })

  it('drops an older page that arrives after navigating to another workload', async () => {
    const pending = deferred()
    await render({ identity: 'A', newest: page('a', 4, 3, true), fetchOlder: () => pending.promise })
    let loading!: Promise<void>
    await act(async () => { loading = latest().loadOlder() })
    await render({ identity: 'B', newest: page('b', 4, 3, true), fetchOlder: () => pending.promise })
    await act(async () => { pending.resolve(page('a', 2, 1, false)); await loading })
    expect(ids()).toEqual(['b3', 'b4'])
    expect(latest().truncated).toBe(true)
    expect(latest().loadingOlder).toBe(false)
  })

  it('drops an older page that arrives after a refresh restarted paging', async () => {
    const first = deferred()
    let calls = 0
    const fetchOlder = () => (++calls === 1 ? Promise.resolve(page('', 20, 11, true)) : first.promise)
    await render({ identity: 'A', newest: page('', 30, 21, true), fetchOlder })
    await act(async () => latest().loadOlder())
    let loading!: Promise<void>
    await act(async () => { loading = latest().loadOlder() })
    expect(latest().loadingOlder).toBe(true)
    // More arrived than one page holds: the refreshed page no longer reaches what's loaded.
    await render({ identity: 'A', newest: page('', 60, 51, true), fetchOlder })
    expect(latest().loadingOlder).toBe(false)
    await act(async () => { first.resolve(page('', 10, 1, false)); await loading })
    expect(ids()).toEqual(page('', 60, 51, true).events.map((e) => e.id).sort())
    expect(latest().truncated).toBe(true)
  })
})
