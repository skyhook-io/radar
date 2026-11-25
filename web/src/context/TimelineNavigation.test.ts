import { describe, expect, it, vi } from 'vitest'
import { canOpenTimelineEvent, openTimelineEventIn, type TimelineScopeControls } from './TimelineNavigation'

const namespaced = { eventId: 'ev-cm', namespace: 'shop', timestamp: '2026-11-18T12:00:00Z' }
const clusterScoped = { eventId: 'ev-hook', namespace: '', timestamp: '2026-11-18T12:00:00Z' }

// The pick request behaves like the namespace mutation: the server's answer
// arrives later, and a refusal (Radar Hub's proxy passes GET only) rejects.
function controls(current: string[], serverAccepts: boolean) {
  const navigate = vi.fn<(search: URLSearchParams) => void>()
  const setNamespaces = vi.fn<(namespaces: string[]) => void>()
  const answers: Promise<string[]>[] = []
  const c: TimelineScopeControls = {
    currentNamespaces: current,
    cacheScoped: false,
    setNamespaces,
    requestNamespaces: (picked) => {
      const answer = serverAccepts ? Promise.resolve(picked) : Promise.reject(new Error('HTTP 405: method not allowed'))
      answer.catch(() => {})
      answers.push(answer)
    },
    navigate,
  }
  const settled = () => Promise.allSettled(answers)
  return { c, navigate, setNamespaces, answers, settled }
}

describe('openTimelineEventIn', () => {
  it.each([true, false])('opens a namespaced event scoped to its namespace (server accepts the pick: %s)', async (accepts) => {
    const { c, navigate, setNamespaces, answers, settled } = controls(['gate'], accepts)
    openTimelineEventIn(namespaced, c)
    expect(answers).toHaveLength(1)
    expect((await settled())[0].status).toBe(accepts ? 'fulfilled' : 'rejected')
    expect(navigate).toHaveBeenCalledTimes(1)
    expect(setNamespaces).toHaveBeenCalledWith(['shop'])
    expect(navigate.mock.calls[0][0].get('namespaces')).toBe('shop')
    expect(navigate.mock.calls[0][0].get('event')).toBe('ev-cm')
  })

  it.each([true, false])('opens a cluster-scoped event in all namespaces, even when the server refuses the pick (accepts: %s)', async (accepts) => {
    const { c, navigate, setNamespaces, answers, settled } = controls(['shop'], accepts)
    openTimelineEventIn(clusterScoped, c)
    expect(answers).toHaveLength(1)
    expect((await settled())[0].status).toBe(accepts ? 'fulfilled' : 'rejected')
    expect(setNamespaces).toHaveBeenCalledTimes(1)
    expect(setNamespaces).toHaveBeenCalledWith([])
    expect(navigate).toHaveBeenCalledTimes(1)
    expect(navigate.mock.calls[0][0].has('namespaces')).toBe(false)
    expect(navigate.mock.calls[0][0].get('event')).toBe('ev-hook')
  })
})

describe('canOpenTimelineEvent', () => {
  it('offers a cluster-scoped event only when the view can widen to all namespaces', () => {
    expect(canOpenTimelineEvent(namespaced, true)).toBe(true)
    expect(canOpenTimelineEvent(clusterScoped, false)).toBe(true)
    expect(canOpenTimelineEvent(clusterScoped, true)).toBe(false)
  })
})
