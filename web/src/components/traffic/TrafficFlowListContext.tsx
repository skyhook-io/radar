import { useEffect, useState, type ReactNode } from 'react'
import type { TrafficFlow } from '../../types'
import type { TrafficGraphSelection } from './TrafficGraph'

interface TrafficFlowListContextValue {
  flows: TrafficFlow[]
  responsesCallerOriented: boolean
  graphSelection: TrafficGraphSelection | null
  clearSelection: () => void
  /** The records for the current selection are still being fetched. */
  loading?: boolean
  /** Says which records the list holds when it is not all of them. */
  note?: string
}

const emptyValue: TrafficFlowListContextValue = { flows: [], responsesCallerOriented: false, graphSelection: null, clearSelection: () => {} }

// Module-level store — TrafficView writes, dock tab reads.
let currentValue: TrafficFlowListContextValue = emptyValue
const listeners = new Set<() => void>()

function setValue(val: TrafficFlowListContextValue) {
  currentValue = val
  listeners.forEach(fn => fn())
}

export function useTrafficFlowList(): TrafficFlowListContextValue {
  const [, forceUpdate] = useState(0)
  useEffect(() => {
    const listener = () => forceUpdate(n => n + 1)
    listeners.add(listener)
    return () => { listeners.delete(listener) }
  }, [])
  return currentValue
}

// Separate search state shared between dock header and flow list
let searchValue = ''
const searchListeners = new Set<() => void>()

export function setFlowSearch(val: string) {
  searchValue = val
  searchListeners.forEach(fn => fn())
}

export function useFlowSearch(): [string, (val: string) => void] {
  const [, forceUpdate] = useState(0)
  useEffect(() => {
    const listener = () => forceUpdate(n => n + 1)
    searchListeners.add(listener)
    return () => { searchListeners.delete(listener) }
  }, [])
  return [searchValue, setFlowSearch]
}

// Provider — call this from TrafficView to publish flow data
export function TrafficFlowListProvider({
  flows,
  responsesCallerOriented,
  graphSelection,
  clearSelection,
  loading,
  note,
  children,
}: TrafficFlowListContextValue & { children: ReactNode }) {
  useEffect(() => {
    setValue({ flows, responsesCallerOriented, graphSelection, clearSelection, loading, note })
  }, [flows, responsesCallerOriented, graphSelection, clearSelection, loading, note])

  useEffect(() => {
    return () => {
      setValue(emptyValue)
      setFlowSearch('')
    }
  }, [])

  return <>{children}</>
}
