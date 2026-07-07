import { useEffect, useMemo, useRef } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useDashboard, type DashboardResponse } from '../api/client'
import { dashboardClusterLoadState, idleClusterLoadState, type ClusterLoadState } from '../types/clusterLoadState'

interface UseClusterLoadStateArgs {
  namespaces: string[]
  mainView: string
  chromeless: boolean
  contentReady: boolean
  onClusterLoadStateChange?: (state: ClusterLoadState) => void
}

interface UseClusterLoadStateResult {
  clusterLoadState: ClusterLoadState
  showHomeClusterLoadFallback: boolean
  // The dashboard's first fetch is still in flight (no data yet). In this phase a
  // center "Loading dashboard…" splash is shown, so the topbar label would be
  // redundant — callers can suppress it and keep just the status dot.
  clusterLoadInitial: boolean
}

// Tracks cluster-data warmup (deferred/partial dashboard load) once the main
// connection is usable. Standalone / embedded-with-chrome render it in Radar's
// topbar; chromeless hosts (Radar Hub) receive it via onClusterLoadStateChange;
// a chromeless host without a callback gets a fallback row on Home.
export function useClusterLoadState({
  namespaces,
  mainView,
  chromeless,
  contentReady,
  onClusterLoadStateChange,
}: UseClusterLoadStateArgs): UseClusterLoadStateResult {
  const showHomeClusterLoadFallback = chromeless && !onClusterLoadStateChange && mainView === 'home'
  const needsClusterLoadState =
    contentReady && (!chromeless || Boolean(onClusterLoadStateChange) || mainView === 'home')

  // Off Home, stop observing once warmup has settled so we don't keep refetching
  // /dashboard on other views. `enabled: false` makes the observer inactive,
  // which also drops it from the SSE-driven invalidateQueries(['dashboard'])
  // refetches — a stopped refetchInterval alone would not. Home stays live via
  // HomeView's own observer; returning Home or changing scope re-arms, since
  // `enabled` is derived from the current cached (re)load state.
  const queryClient = useQueryClient()
  const cached = queryClient.getQueryState<DashboardResponse>(['dashboard', namespaces])
  const warmupSettled =
    cached?.status === 'error' || (cached?.data != null && !dashboardClusterLoadState(cached.data).loading)
  const enabled = needsClusterLoadState && (mainView === 'home' || !warmupSettled)

  const { data, isPending, isError } = useDashboard(namespaces, { enabled })

  const clusterLoadState = useMemo<ClusterLoadState>(() => {
    if (!needsClusterLoadState || isError) return idleClusterLoadState
    if (data) return dashboardClusterLoadState(data)
    if (isPending) return { loading: true, message: 'Loading dashboard…', pendingKinds: [] }
    return idleClusterLoadState
  }, [needsClusterLoadState, isError, data, isPending])

  // Ref the host callback so an unmemoized prop doesn't churn emits, and so the
  // unmount reset fires only on real unmount (not on every callback identity change).
  const emitRef = useRef(onClusterLoadStateChange)
  useEffect(() => {
    emitRef.current = onClusterLoadStateChange
  })
  useEffect(() => {
    emitRef.current?.(clusterLoadState)
  }, [clusterLoadState])
  useEffect(() => () => emitRef.current?.(idleClusterLoadState), [])

  const clusterLoadInitial = clusterLoadState.loading && !data

  return { clusterLoadState, showHomeClusterLoadFallback, clusterLoadInitial }
}
