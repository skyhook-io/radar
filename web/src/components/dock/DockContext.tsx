import { createContext, useContext, useState, useCallback, ReactNode } from 'react'

export type DockTabType = 'terminal' | 'logs'

export interface DockTab {
  id: string
  type: DockTabType
  title: string
  // Terminal props
  namespace?: string
  podName?: string
  containerName?: string
  containers?: string[]
  // Logs props
  // (namespace, podName, containers already covered)
}

interface DockContextValue {
  tabs: DockTab[]
  activeTabId: string | null
  isExpanded: boolean
  addTab: (tab: Omit<DockTab, 'id'>) => string
  removeTab: (id: string) => void
  setActiveTab: (id: string) => void
  toggleExpanded: () => void
  setExpanded: (expanded: boolean) => void
  closeAll: () => void
}

const DockContext = createContext<DockContextValue | null>(null)

let tabIdCounter = 0

export function DockProvider({ children }: { children: ReactNode }) {
  const [tabs, setTabs] = useState<DockTab[]>([])
  const [activeTabId, setActiveTabId] = useState<string | null>(null)
  const [isExpanded, setIsExpanded] = useState(false)

  const addTab = useCallback((tabData: Omit<DockTab, 'id'>) => {
    // Check if a similar tab already exists
    const existingTab = tabs.find(t =>
      t.type === tabData.type &&
      t.namespace === tabData.namespace &&
      t.podName === tabData.podName &&
      t.containerName === tabData.containerName
    )

    if (existingTab) {
      setActiveTabId(existingTab.id)
      setIsExpanded(true)
      return existingTab.id
    }

    const id = `dock-tab-${++tabIdCounter}`
    const newTab: DockTab = { ...tabData, id }

    setTabs(prev => [...prev, newTab])
    setActiveTabId(id)
    setIsExpanded(true)

    return id
  }, [tabs])

  const removeTab = useCallback((id: string) => {
    setTabs(prev => {
      const newTabs = prev.filter(t => t.id !== id)

      // If we're removing the active tab, switch to another
      if (activeTabId === id && newTabs.length > 0) {
        setActiveTabId(newTabs[newTabs.length - 1].id)
      } else if (newTabs.length === 0) {
        setActiveTabId(null)
        setIsExpanded(false)
      }

      return newTabs
    })
  }, [activeTabId])

  const setActiveTab = useCallback((id: string) => {
    setActiveTabId(id)
  }, [])

  const toggleExpanded = useCallback(() => {
    setIsExpanded(prev => !prev)
  }, [])

  const closeAll = useCallback(() => {
    setTabs([])
    setActiveTabId(null)
    setIsExpanded(false)
  }, [])

  return (
    <DockContext.Provider value={{
      tabs,
      activeTabId,
      isExpanded,
      addTab,
      removeTab,
      setActiveTab,
      toggleExpanded,
      setExpanded: setIsExpanded,
      closeAll,
    }}>
      {children}
    </DockContext.Provider>
  )
}

export function useDock() {
  const context = useContext(DockContext)
  if (!context) {
    throw new Error('useDock must be used within a DockProvider')
  }
  return context
}

// Convenience hooks for adding specific tab types
export function useOpenTerminal() {
  const { addTab } = useDock()

  // Return a function that opens a terminal tab
  // Note: addTab is already memoized in the provider
  const openTerminal = (opts: {
    namespace: string
    podName: string
    containerName: string
    containers: string[]
  }) => {
    addTab({
      type: 'terminal',
      title: `${opts.podName}/${opts.containerName}`,
      namespace: opts.namespace,
      podName: opts.podName,
      containerName: opts.containerName,
      containers: opts.containers,
    })
  }

  return openTerminal
}

export function useOpenLogs() {
  const { addTab } = useDock()

  // Return a function that opens a logs tab
  // Note: addTab is already memoized in the provider
  const openLogs = (opts: {
    namespace: string
    podName: string
    containers: string[]
    containerName?: string
  }) => {
    const title = opts.containerName
      ? `${opts.podName}/${opts.containerName}`
      : opts.podName

    addTab({
      type: 'logs',
      title,
      namespace: opts.namespace,
      podName: opts.podName,
      containerName: opts.containerName,
      containers: opts.containers,
    })
  }

  return openLogs
}
