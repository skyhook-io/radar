import { describe, expect, it } from 'vitest'
import type { ActivityFilterKey, TimelineGrouping, TimelineSort } from '@skyhook-io/k8s-ui'
import {
  parseTimeMode,
  resolveApplicationTimelineScope,
  writeTimelineParams,
  onlyHighFreqDiffer,
  timeModeEqual,
  type TimelineMode,
  type PersistedTimelineState,
  type TimelineViewMode,
} from './TimelineView'

// These mirror the module-private defaults writeTimelineParams omits at (and
// parseTimeMode falls back to). Kept in sync deliberately: the round-trip tests
// below fail loudly if a module default drifts from what is asserted here.
const DEFAULT_LIVE_WIDTH_MS = 60 * 60 * 1000
const DAY_MS = 24 * 60 * 60 * 1000
const DEFAULT_VIEW: TimelineViewMode = 'swimlane'
const DEFAULT_GROUPING: TimelineGrouping = 'app'
const DEFAULT_SORT: TimelineSort = 'importance'
const ACTIVITY_KEYS: readonly ActivityFilterKey[] = ['changes', 'k8s_events', 'warnings', 'unhealthy']
const GROUPINGS: readonly TimelineGrouping[] = ['app', 'owner', 'flat']
const SORTS: readonly TimelineSort[] = ['importance', 'recent', 'name']

const live = (widthMs: number, all?: boolean): TimelineMode => (all ? { kind: 'live', widthMs, all } : { kind: 'live', widthMs })
const frozen = (fromMs: number, toMs: number): TimelineMode => ({ kind: 'frozen', fromMs, toMs })
const sp = (obj: Record<string, string>) => new URLSearchParams(obj)

// The inverse of writeTimelineParams, reconstructed from the component's own
// URL->state read (TimelineView's sync effect). Uses the exported parseTimeMode
// for the time mode; the remaining fields parse inline the same way the
// component does, so parse(write(state)) exercises the real deep-link contract.
const parseEnum = <T extends string>(value: string | null, allowed: readonly T[], fallback: T): T =>
  value != null && (allowed as readonly string[]).includes(value) ? (value as T) : fallback

function parseState(
  params: URLSearchParams,
  opts: { isRetained: boolean; requiresNamespaceFilter: boolean; maxRangeDays?: number; hasPins: boolean },
): PersistedTimelineState {
  const view = params.get('view')
  const viewMode: TimelineViewMode = opts.requiresNamespaceFilter
    ? 'list'
    : view === 'list' || view === 'swimlane'
      ? view
      : DEFAULT_VIEW
  const activityRaw = params.get('activity')
  const activityFilter =
    activityRaw == null
      ? []
      : activityRaw
          .split(',')
          .map((s) => s.trim())
          .filter((s): s is ActivityFilterKey => (ACTIVITY_KEYS as readonly string[]).includes(s))
  const kindsRaw = params.get('kinds')
  const kindFilter = kindsRaw ? kindsRaw.split(',').map((s) => s.trim()).filter(Boolean) : []
  return {
    viewMode,
    mode: parseTimeMode(params, opts.isRetained, opts.maxRangeDays),
    showDeleted: params.get('deleted') !== '0',
    pinnedOnly: params.get('pinnedOnly') === '1' && opts.hasPins,
    search: params.get('q') ?? '',
    activityFilter,
    kindFilter,
    grouping: parseEnum(params.get('grouping'), GROUPINGS, DEFAULT_GROUPING),
    sort: parseEnum(params.get('sort'), SORTS, DEFAULT_SORT),
    selectedEventId: params.get('event'),
  }
}

const defaultState: PersistedTimelineState = {
  viewMode: DEFAULT_VIEW,
  mode: live(DEFAULT_LIVE_WIDTH_MS),
  showDeleted: true,
  pinnedOnly: false,
  search: '',
  activityFilter: [],
  kindFilter: [],
  grouping: DEFAULT_GROUPING,
  sort: DEFAULT_SORT,
  selectedEventId: null,
}

const sortedEntries = (params: URLSearchParams) => [...params.entries()].sort(([a], [b]) => a.localeCompare(b))

describe('parseTimeMode', () => {
  it('round-trips the live-window presets encoded in ?window', () => {
    expect(parseTimeMode(sp({ window: 'all' }), true)).toEqual(live(DEFAULT_LIVE_WIDTH_MS, true))
    expect(parseTimeMode(sp({ window: '1800000' }), true)).toEqual(live(1_800_000))
  })

  it('parses an explicit from/to into a frozen window', () => {
    const from = 1_700_000_000_000
    const to = from + 2 * DAY_MS
    expect(parseTimeMode(sp({ from: String(from), to: String(to) }), true)).toEqual(frozen(from, to))
  })

  it('rejects from >= to and falls back to the default live window', () => {
    const t = 1_700_000_000_000
    expect(parseTimeMode(sp({ from: String(t), to: String(t) }), true)).toEqual(live(DEFAULT_LIVE_WIDTH_MS))
    expect(parseTimeMode(sp({ from: String(t + 1000), to: String(t) }), true)).toEqual(live(DEFAULT_LIVE_WIDTH_MS))
  })

  it('caps an out-of-retention from to maxRangeDays before to', () => {
    const to = 10 * DAY_MS
    expect(parseTimeMode(sp({ from: '1', to: String(to) }), true, 7)).toEqual(frozen(to - 7 * DAY_MS, to))
  })

  it('leaves from uncapped when already within maxRangeDays', () => {
    const to = 10 * DAY_MS
    const from = to - 3 * DAY_MS
    expect(parseTimeMode(sp({ from: String(from), to: String(to) }), true, 7)).toEqual(frozen(from, to))
  })

  it('falls back to the default mode on garbage from/to/window', () => {
    expect(parseTimeMode(sp({ from: 'abc', to: 'def' }), true)).toEqual(live(DEFAULT_LIVE_WIDTH_MS))
    expect(parseTimeMode(sp({ from: '1.5', to: '9.5' }), true)).toEqual(live(DEFAULT_LIVE_WIDTH_MS))
    expect(parseTimeMode(sp({ window: 'abc' }), true)).toEqual(live(DEFAULT_LIVE_WIDTH_MS))
    expect(parseTimeMode(sp({ window: '0' }), true)).toEqual(live(DEFAULT_LIVE_WIDTH_MS))
    expect(parseTimeMode(sp({ window: '-5' }), true)).toEqual(live(DEFAULT_LIVE_WIDTH_MS))
  })

  it('ignores from/to/window entirely when the source is not retained', () => {
    const from = 1_700_000_000_000
    expect(
      parseTimeMode(sp({ from: String(from), to: String(from + DAY_MS), window: 'all' }), false),
    ).toEqual(live(DEFAULT_LIVE_WIDTH_MS))
  })
})

describe('writeTimelineParams', () => {
  const retainedOpts = { isRetained: true, requiresNamespaceFilter: false as boolean | undefined }

  it('writes no params for the pristine default state', () => {
    const written = writeTimelineParams(new URLSearchParams(), defaultState, retainedOpts)
    expect(written.toString()).toBe('')
  })

  it('writes exactly the expected keys for a fully non-default state', () => {
    const state: PersistedTimelineState = {
      viewMode: 'list',
      mode: live(6 * 60 * 60 * 1000),
      showDeleted: false,
      pinnedOnly: true,
      search: 'nginx',
      activityFilter: ['warnings', 'unhealthy'],
      kindFilter: ['Pod', 'Deployment'],
      grouping: 'owner',
      sort: 'recent',
      selectedEventId: 'evt-42',
    }
    const written = writeTimelineParams(new URLSearchParams(), state, retainedOpts)
    expect(sortedEntries(written)).toEqual([
      ['activity', 'warnings,unhealthy'],
      ['deleted', '0'],
      ['event', 'evt-42'],
      ['grouping', 'owner'],
      ['kinds', 'Pod,Deployment'],
      ['pinnedOnly', '1'],
      ['q', 'nginx'],
      ['sort', 'recent'],
      ['view', 'list'],
      ['window', '21600000'],
    ])
  })

  it('encodes a frozen window as from/to and omits window', () => {
    const from = 1_700_000_000_000
    const to = from + 2 * DAY_MS
    const written = writeTimelineParams(new URLSearchParams(), { ...defaultState, mode: frozen(from, to) }, retainedOpts)
    expect(written.get('from')).toBe(String(from))
    expect(written.get('to')).toBe(String(to))
    expect(written.has('window')).toBe(false)
  })

  it('preserves foreign params, including application scope, and strips the legacy filter seed', () => {
    const base = new URLSearchParams({
      tab: 'topology',
      app: 'staging/Deployment/api',
      scopeNamespaces: 'argocd,staging',
      filter: 'warnings',
    })
    const written = writeTimelineParams(base, defaultState, retainedOpts)
    expect(written.get('tab')).toBe('topology')
    expect(written.get('app')).toBe('staging/Deployment/api')
    expect(written.get('scopeNamespaces')).toBe('argocd,staging')
    expect(written.has('filter')).toBe(false)
  })

  it('does not persist the list view a large cluster forces', () => {
    const written = writeTimelineParams(new URLSearchParams(), { ...defaultState, viewMode: 'list' }, {
      isRetained: true,
      requiresNamespaceFilter: true,
    })
    expect(written.has('view')).toBe(false)
  })
})

describe('resolveApplicationTimelineScope', () => {
  it('uses the current namespace selection outside application scope', () => {
    expect(resolveApplicationTimelineScope(new URLSearchParams(), ['default', 'staging'])).toEqual({
      appKey: null,
      namespaces: ['default', 'staging'],
      ready: true,
    })
  })

  it('uses the application scope namespaces and removes duplicates', () => {
    const params = sp({
      app: '/Application/radar-hub-staging',
      scopeNamespaces: 'argocd, staging,argocd',
    })

    expect(resolveApplicationTimelineScope(params, ['default'])).toEqual({
      appKey: '/Application/radar-hub-staging',
      namespaces: ['argocd', 'staging'],
      ready: true,
    })
  })

  it('fails closed when an application link omits its namespace scope', () => {
    const params = sp({ app: '/Application/radar-hub-staging' })

    expect(resolveApplicationTimelineScope(params, ['default'])).toEqual({
      appKey: '/Application/radar-hub-staging',
      namespaces: [],
      ready: false,
    })
  })
})

describe('parse(write(state)) round-trip', () => {
  const opts = { isRetained: true, requiresNamespaceFilter: false, maxRangeDays: undefined, hasPins: true }
  const roundTrip = (state: PersistedTimelineState) => {
    const written = writeTimelineParams(new URLSearchParams(), state, {
      isRetained: opts.isRetained,
      requiresNamespaceFilter: opts.requiresNamespaceFilter,
    })
    return parseState(new URLSearchParams(written.toString()), opts)
  }

  it('round-trips the pristine default state', () => {
    expect(roundTrip(defaultState)).toEqual(defaultState)
  })

  it('round-trips a live preset with every control set', () => {
    const state: PersistedTimelineState = {
      viewMode: 'list',
      mode: live(6 * 60 * 60 * 1000),
      showDeleted: false,
      pinnedOnly: true,
      search: 'nginx',
      activityFilter: ['warnings', 'unhealthy'],
      kindFilter: ['Pod', 'Deployment'],
      grouping: 'owner',
      sort: 'recent',
      selectedEventId: 'evt-42',
    }
    expect(roundTrip(state)).toEqual(state)
  })

  it('round-trips a frozen window with a selected event', () => {
    const from = 1_700_000_000_000
    const state: PersistedTimelineState = {
      ...defaultState,
      mode: frozen(from, from + 2 * DAY_MS),
      selectedEventId: 'evt-frozen',
    }
    expect(roundTrip(state)).toEqual(state)
  })

  it('round-trips a whole-span live window and a search with a space', () => {
    const state: PersistedTimelineState = {
      ...defaultState,
      mode: live(DEFAULT_LIVE_WIDTH_MS, true),
      search: 'foo bar',
      activityFilter: ['changes'],
      sort: 'name',
    }
    expect(roundTrip(state)).toEqual(state)
  })
})

describe('timeModeEqual', () => {
  it('is reflexive for live and frozen modes', () => {
    expect(timeModeEqual(live(DEFAULT_LIVE_WIDTH_MS), live(DEFAULT_LIVE_WIDTH_MS))).toBe(true)
    expect(timeModeEqual(frozen(1, 2), frozen(1, 2))).toBe(true)
  })

  it('treats the whole-span all flag as significant', () => {
    expect(timeModeEqual(live(DEFAULT_LIVE_WIDTH_MS), live(DEFAULT_LIVE_WIDTH_MS, true))).toBe(false)
    expect(timeModeEqual(live(DEFAULT_LIVE_WIDTH_MS, true), live(DEFAULT_LIVE_WIDTH_MS, true))).toBe(true)
  })

  it('is false across the live/frozen boundary', () => {
    expect(timeModeEqual(live(DEFAULT_LIVE_WIDTH_MS), frozen(0, DEFAULT_LIVE_WIDTH_MS))).toBe(false)
  })

  it('distinguishes different widths and different frozen windows', () => {
    expect(timeModeEqual(live(DEFAULT_LIVE_WIDTH_MS), live(2 * DEFAULT_LIVE_WIDTH_MS))).toBe(false)
    expect(timeModeEqual(frozen(1, 2), frozen(1, 3))).toBe(false)
  })
})

describe('onlyHighFreqDiffer (history replace vs push)', () => {
  it('replaces when only a high-frequency key differs (window pan/zoom, search, drawer event)', () => {
    expect(onlyHighFreqDiffer('window=3600000', 'window=7200000')).toBe(true)
    expect(onlyHighFreqDiffer('q=a', 'q=b')).toBe(true)
    expect(onlyHighFreqDiffer('event=evt-1', 'event=evt-2')).toBe(true)
  })

  it('replaces on a live<->frozen transition — every time-window key is high-frequency', () => {
    expect(onlyHighFreqDiffer('window=7200000', 'from=1&to=2')).toBe(true)
    expect(onlyHighFreqDiffer('', 'from=1&to=2')).toBe(true)
  })

  it('pushes when a discrete control changes', () => {
    expect(onlyHighFreqDiffer('view=list', 'view=swimlane')).toBe(false)
    expect(onlyHighFreqDiffer('', 'grouping=owner')).toBe(false)
    expect(onlyHighFreqDiffer('', 'sort=recent')).toBe(false)
    expect(onlyHighFreqDiffer('', 'activity=warnings')).toBe(false)
    expect(onlyHighFreqDiffer('', 'kinds=Pod')).toBe(false)
    expect(onlyHighFreqDiffer('', 'deleted=0')).toBe(false)
    expect(onlyHighFreqDiffer('', 'pinnedOnly=1')).toBe(false)
  })

  it('pushes when a diff mixes a high-frequency and a discrete key', () => {
    expect(onlyHighFreqDiffer('', 'window=7200000&grouping=owner')).toBe(false)
  })

  it('is not replace-worthy when nothing differs', () => {
    expect(onlyHighFreqDiffer('window=3600000', 'window=3600000')).toBe(false)
    expect(onlyHighFreqDiffer('', '')).toBe(false)
  })
})
