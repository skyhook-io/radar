import { createContext, useContext, useState, useCallback, ReactNode } from 'react'

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

  return (
    <ContextSwitchContext.Provider value={{ isSwitching, targetContext, progressMessage, startSwitch, updateProgress, endSwitch }}>
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
