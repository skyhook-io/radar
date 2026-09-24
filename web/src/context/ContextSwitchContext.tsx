import { createContext, useContext, useState, useCallback, useRef, ReactNode } from 'react'
import type { SelectedResource } from '../types'

export interface ParsedContextInfo {
  raw: string
  provider: string | null
  account: string | null
  region: string | null
  clusterName: string
}

interface ContextSwitchState {
  isSwitching: boolean
  targetContext: ParsedContextInfo | null
  progressMessage: string | null
  startSwitch: (context: ParsedContextInfo) => void
  updateProgress: (message: string) => void
  endSwitch: () => void
  // A resource to open once a switch to `context` is live. The app's context
  // reset takes it, so the open can't race the reset that clears the old
  // cluster's drawer and URL. A change to any other context discards it.
  setOpenAfterSwitch: (target: { context: string; resource: SelectedResource } | null) => void
  takeOpenAfterSwitch: (context: string) => SelectedResource | null
}

const ContextSwitchContext = createContext<ContextSwitchState | null>(null)

export function ContextSwitchProvider({ children }: { children: ReactNode }) {
  const [isSwitching, setIsSwitching] = useState(false)
  const [targetContext, setTargetContext] = useState<ParsedContextInfo | null>(null)
  const [progressMessage, setProgressMessage] = useState<string | null>(null)

  const startSwitch = useCallback((context: ParsedContextInfo) => {
    setIsSwitching(true)
    setTargetContext(context)
    setProgressMessage(null)
  }, [])

  const updateProgress = useCallback((message: string) => {
    setProgressMessage(message)
  }, [])

  const endSwitch = useCallback(() => {
    setIsSwitching(false)
    setTargetContext(null)
    setProgressMessage(null)
  }, [])

  const openAfterSwitch = useRef<{ context: string; resource: SelectedResource } | null>(null)
  const setOpenAfterSwitch = useCallback((target: { context: string; resource: SelectedResource } | null) => {
    openAfterSwitch.current = target
  }, [])
  const takeOpenAfterSwitch = useCallback((context: string) => {
    const target = openAfterSwitch.current
    openAfterSwitch.current = null
    return target && target.context === context ? target.resource : null
  }, [])

  return (
    <ContextSwitchContext.Provider value={{ isSwitching, targetContext, progressMessage, startSwitch, updateProgress, endSwitch, setOpenAfterSwitch, takeOpenAfterSwitch }}>
      {children}
    </ContextSwitchContext.Provider>
  )
}

export function useContextSwitch() {
  const context = useContext(ContextSwitchContext)
  if (!context) {
    throw new Error('useContextSwitch must be used within ContextSwitchProvider')
  }
  return context
}
