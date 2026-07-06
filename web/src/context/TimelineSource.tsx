// Provides the active timeline data source to the timeline wrappers.
//
// Radar's binary never sets a source, so the default is the local event store
// (GET {apiBase}/changes) — unchanged OSS behavior. A host embedding RadarApp
// behind a proxy that serves retained history passes `timelineSource` on
// RadarApp; this provider resolves it once and hands the timeline wrappers a
// source-agnostic `useEvents` hook.
//
// Default (no provider): the local source, so components work standalone.
import { createContext, useContext, useMemo } from 'react'
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
  const source = useMemo(
    () => resolveTimelineSource(config),
    [config?.mode, config?.maxRangeDays],
  )
  return (
    <TimelineSourceContext.Provider value={source}>
      {children}
    </TimelineSourceContext.Provider>
  )
}

export function useTimelineSource(): TimelineSource {
  return useContext(TimelineSourceContext)
}
