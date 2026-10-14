import { afterEach, describe, expect, it, vi } from 'vitest'
import { createKindFilterStore } from './kindFilterStore'

const storage = new Map<string, string>()
vi.stubGlobal('localStorage', {
  getItem: (k: string) => storage.get(k) ?? null,
  setItem: (k: string, v: string) => void storage.set(k, v),
  removeItem: (k: string) => void storage.delete(k),
  get length() { return storage.size },
  key: (i: number) => [...storage.keys()][i] ?? null,
})
afterEach(() => {
  storage.clear()
  vi.stubGlobal('sessionStorage', undefined)
})

function fakeSessionStorage() {
  const items = new Map<string, string>()
  return {
    getItem: (k: string) => items.get(k) ?? null,
    setItem: (k: string, v: string) => void items.set(k, v),
    get length() { return items.size },
    key: (i: number) => [...items.keys()][i] ?? null,
  }
}

describe('createKindFilterStore', () => {
  it('keeps each cluster and each API group apart', () => {
    const a = createKindFilterStore('/api|ctx-a')
    const b = createKindFilterStore('/api|ctx-b')
    a.set('services', '', 'filters=type%3AClusterIP')
    a.set('services', 'serving.knative.dev', 'problems=failed')

    expect(a.get('services', '')).toBe('filters=type%3AClusterIP')
    expect(a.get('services', 'serving.knative.dev')).toBe('problems=failed')
    expect(b.get('services', '')).toBeUndefined()
  })

  it('removes the entry when the filters are cleared', () => {
    const store = createKindFilterStore('/api|ctx')
    store.set('pods', '', 'problems=failed')
    store.set('pods', '', '')

    expect(store.get('pods', '')).toBeUndefined()
    expect(storage.size).toBe(0)
  })

  it('remembers the last Resources list per cluster, with its API group only', () => {
    const a = createKindFilterStore('/api|ctx-a')
    a.recordLastKind('/resources/services', '?apiGroup=serving.knative.dev&filters=x&namespaces=n')
    a.recordLastKind('/resources', '')
    a.recordLastKind('/helm', '')

    expect(a.lastKind()).toBe('/resources/services?apiGroup=serving.knative.dev')
    expect(createKindFilterStore('/api|ctx-b').lastKind()).toBeUndefined()

    a.recordLastKind('/resources/pods/', '?problems=failed')
    expect(a.lastKind()).toBe('/resources/pods')
  })

  it('keeps a tab on its own filters while another tab saves different ones', () => {
    const tabA = createKindFilterStore('/api|ctx')
    const tabB = createKindFilterStore('/api|ctx')
    tabA.set('pods', '', 'filters=namespace%3Aa')
    tabB.set('pods', '', 'filters=namespace%3Ab')

    expect(tabA.get('pods', '')).toBe('filters=namespace%3Aa')
    expect(createKindFilterStore('/api|ctx').get('pods', '')).toBe('filters=namespace%3Ab')

    tabA.set('pods', '', '')
    tabB.set('pods', '', 'filters=namespace%3Ac')
    expect(tabA.get('pods', '')).toBeUndefined()
  })

  it('keeps what a tab restored when another tab saves later', () => {
    createKindFilterStore('/api|ctx').set('pods', '', 'filters=namespace%3Aa')
    const tabA = createKindFilterStore('/api|ctx')
    expect(tabA.get('pods', '')).toBe('filters=namespace%3Aa')
    expect(tabA.get('services', '')).toBeUndefined()

    const tabB = createKindFilterStore('/api|ctx')
    tabB.set('pods', '', 'filters=namespace%3Ab')
    tabB.set('services', '', 'filters=type%3AClusterIP')

    expect(tabA.get('pods', '')).toBe('filters=namespace%3Aa')
    expect(tabA.get('services', '')).toBeUndefined()
  })

  it('clears one column on every kind of the cluster, in the tab and in storage', () => {
    const tab = createKindFilterStore('/api|ctx')
    tab.set('pods', '', 'filters=namespace%3Aa%7Cstatus%3ARunning&problems=failed')
    tab.set('services', '', 'filters=namespace%3Aexclude%3Ab')
    tab.set('deployments', '', 'problems=failed')
    // Another tab saved this kind; this tab never read it.
    createKindFilterStore('/api|ctx').set('configmaps', '', 'filters=namespace%3Ac')
    const otherCluster = createKindFilterStore('/api|ctx:2')
    otherCluster.set('pods', '', 'filters=namespace%3Aa')

    tab.clearColumn('namespace')

    expect(tab.get('pods', '')).toBe('filters=status%3ARunning&problems=failed')
    expect(tab.get('services', '')).toBeUndefined()
    expect(tab.get('deployments', '')).toBe('problems=failed')
    expect(tab.get('configmaps', '')).toBeUndefined()
    const fresh = createKindFilterStore('/api|ctx')
    expect(fresh.get('pods', '')).toBe('filters=status%3ARunning&problems=failed')
    expect(fresh.get('services', '')).toBeUndefined()
    expect(fresh.get('configmaps', '')).toBeUndefined()
    expect(createKindFilterStore('/api|ctx:2').get('pods', '')).toBe('filters=namespace%3Aa')
  })

  it("keeps the tab's own filters on reload and in a duplicated tab; a new tab starts from the last saved", () => {
    const tabA = fakeSessionStorage()
    vi.stubGlobal('sessionStorage', tabA)
    createKindFilterStore('/api|ctx').set('pods', '', 'filters=namespace%3Aa')

    // Tab B saves a newer choice.
    vi.stubGlobal('sessionStorage', fakeSessionStorage())
    createKindFilterStore('/api|ctx').set('pods', '', 'filters=namespace%3Ab')

    vi.stubGlobal('sessionStorage', tabA)
    expect(createKindFilterStore('/api|ctx').get('pods', '')).toBe('filters=namespace%3Aa')

    const duplicate = fakeSessionStorage()
    for (let i = 0; i < tabA.length; i++) duplicate.setItem(tabA.key(i)!, tabA.getItem(tabA.key(i)!)!)
    vi.stubGlobal('sessionStorage', duplicate)
    expect(createKindFilterStore('/api|ctx').get('pods', '')).toBe('filters=namespace%3Aa')

    vi.stubGlobal('sessionStorage', fakeSessionStorage())
    expect(createKindFilterStore('/api|ctx').get('pods', '')).toBe('filters=namespace%3Ab')
  })

  it('remembers filters in the tab only when storage is unavailable', () => {
    vi.stubGlobal('localStorage', {
      getItem: () => { throw new Error('denied') },
      setItem: () => { throw new Error('denied') },
      removeItem: () => { throw new Error('denied') },
    })
    const store = createKindFilterStore('/api|ctx')
    store.set('pods', '', 'problems=failed')
    store.recordLastKind('/resources/pods', '')

    expect(store.get('pods', '')).toBe('problems=failed')
    expect(createKindFilterStore('/api|ctx').get('pods', '')).toBeUndefined()
    expect(store.lastKind()).toBeUndefined()
  })
})
