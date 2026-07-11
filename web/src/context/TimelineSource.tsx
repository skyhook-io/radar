// Provides the active timeline data source to the timeline wrappers.
//
// Radar's binary never sets a source, so the default is the local event store
// (GET {apiBase}/changes) — unchanged OSS behavior. A host embedding RadarApp
// behind a proxy that serves retained history passes `timelineSource` on
// RadarApp; this provider resolves it once and hands the timeline wrappers a
// source-agnostic `useEvents` hook.
//
// Default (no provider): the local source, so components work standalone.
import { createContext, useContext, useMemo, Fragment } from 'react'
import type { ReactNode } from 'react'
import {
  localSource,
  resolveTimelineSource,
  type TimelineSource,
  type TimelineSourceConfig,
} from '../api/timelineSource'

const TimelineSourceContext = createContext<TimelineSource>(localSource)

export function TimelineSourceProvider({
  config,
  children,
}: {
  config?: TimelineSourceConfig
  children: ReactNode
}) {
  // Resolve from the config's two fields, not the object: a host passing a
  // fresh config literal each render must not re-resolve the source.
  const mode = config?.mode
  const maxRangeDays = config?.maxRangeDays
  const source = useMemo(
    () => resolveTimelineSource(mode === 'retained' ? { mode, maxRangeDays } : undefined),
    [mode, maxRangeDays],
  )
  return (
    <TimelineSourceContext.Provider value={source}>
      {/* Key the subtree on the source mode. `useEvents` is a hook, and the local
          vs retained sources are different hook functions with different internal
          hook counts; a host flipping `timelineSource` mode mid-session would
          otherwise corrupt React's hook order. Remounting on the flip keeps the
          hook sequence consistent. */}
      <Fragment key={mode ?? 'local'}>{children}</Fragment>
    </TimelineSourceContext.Provider>
  )
}

export function useTimelineSource(): TimelineSource {
  return useContext(TimelineSourceContext)
}
