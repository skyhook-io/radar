import { createContext, useContext, useState, useCallback, useRef, useEffect, ReactNode } from 'react'

export type DockTabType = 'terminal' | 'logs' | 'workload-logs' | 'node-terminal' | 'local-terminal' | 'traffic-flows'

export interface DockTab {
  id: string
  type: DockTabType
  title: string
  // Common app context (optional — app-specific, used by tab content components)
  orgId?: string
  clusterId?: string
  clusterName?: string
  // Terminal / logs props
  namespace?: string
  podName?: string
  containerName?: string
  containers?: string[]
  // Workload logs props
  workloadKind?: string
  workloadName?: string
  // Node terminal props
  nodeName?: string
  // Local terminal props
  initialCommand?: string
}

export interface DockContextValue {
  tabs: DockTab[]
  activeTabId: string | null
  isExpanded: boolean
  leftOffset: number
  height: number
  isMaximized: boolean
  isResizing: boolean
  addTab: (tab: Omit<DockTab, 'id'>) => string
  removeTab: (id: string) => void
  setActiveTab: (id: string) => void
  toggleExpanded: () => void
  setExpanded: (expanded: boolean) => void
  setLeftOffset: (offset: number) => void
  setHeight: (height: number) => void
  setMaximized: (maximized: boolean) => void
  setResizing: (resizing: boolean) => void
  closeAll: () => void
}

const DockContext = createContext<DockContextValue | null>(null)

let tabIdCounter = 0

const DOCK_DEFAULT_HEIGHT = 400
const DOCK_COLLAPSED_HEIGHT = 36

export function DockProvider({ children }: { children: ReactNode }) {
  const [tabs, setTabs] = useState<DockTab[]>([])
  const [activeTabId, setActiveTabId] = useState<string | null>(null)
  const [isExpanded, setIsExpanded] = useState(false)
  const [leftOffset, setLeftOffset] = useState(0)
  const [height, setHeight] = useState(DOCK_DEFAULT_HEIGHT)
  const [isMaximized, setIsMaximized] = useState(false)
  const [isResizing, setIsResizing] = useState(false)
  // Keep a ref to the latest tabs for deduplication without stale-closure issues
  const tabsRef = useRef<DockTab[]>(tabs)
  useEffect(() => { tabsRef.current = tabs }, [tabs])

  const addTab = useCallback((tabData: Omit<DockTab, 'id'>) => {
    const existingTab = tabsRef.current.find(t => {
      if (t.type !== tabData.type) return false
      // Include orgId + clusterId so same-named pods/workloads on different clusters don't deduplicate
      if (t.orgId !== tabData.orgId || t.clusterId !== tabData.clusterId) return false
      if (t.type === 'workload-logs') {
        return t.namespace === tabData.namespace &&
               t.workloadKind === tabData.workloadKind &&
               t.workloadName === tabData.workloadName
      }
      if (t.type === 'node-terminal') {
        return t.nodeName === tabData.nodeName
      }
      if (t.type === 'local-terminal') {
        return false // Allow multiple local terminals
      }
      if (t.type === 'traffic-flows') {
        return true // Singleton — always reuse existing tab
      }
      return t.namespace === tabData.namespace &&
             t.podName === tabData.podName &&
             t.containerName === tabData.containerName
    })

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
  }, [])

  const removeTab = useCallback((id: string) => {
    setTabs(prev => {
      const newTabs = prev.filter(t => t.id !== id)

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
      leftOffset,
      height,
      isMaximized,
      isResizing,
      addTab,
      removeTab,
      setActiveTab,
      toggleExpanded,
      setExpanded: setIsExpanded,
      setLeftOffset,
      setHeight,
      setMaximized: setIsMaximized,
      setResizing: setIsResizing,
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

/**
 * Reserved vertical space at the bottom of the viewport for the dock, in px.
 * Drawers and main-content spacers subtract this so they don't sit behind the dock.
 * Maximized returns 0 — the dock covers everything by design.
 *
 * Safe to call without a DockProvider ancestor (returns 0) — the shared
 * @skyhook/k8s-ui package exports drawers that may be consumed without a dock.
 */
export function useDockReservedHeight(): number {
  const context = useContext(DockContext)
  if (!context) return 0
  const { tabs, isExpanded, isMaximized, height } = context
  if (tabs.length === 0) return 0
  // Order matches BottomDock's effectiveHeight: collapsed tab bar wins over
  // maximized so the (maximized=true, expanded=false) state — reachable by
  // Cmd+J / Ctrl+` while maximized — still reserves the 36px tab bar height.
  if (!isExpanded) return DOCK_COLLAPSED_HEIGHT
  if (isMaximized) return 0
  return height
}

// Convenience hooks for adding specific tab types

export function useOpenTerminal() {
  const { addTab } = useDock()

  return (opts: {
    namespace: string
    podName: string
    containerName: string
    containers: string[]
    orgId?: string
    clusterId?: string
    clusterName?: string
  }) => {
    addTab({
      type: 'terminal',
      title: `${opts.podName}/${opts.containerName}`,
      namespace: opts.namespace,
      podName: opts.podName,
      containerName: opts.containerName,
      containers: opts.containers,
      orgId: opts.orgId,
      clusterId: opts.clusterId,
      clusterName: opts.clusterName,
    })
  }
}

export function useOpenLogs() {
  const { addTab } = useDock()

  return (opts: {
    namespace: string
    podName: string
    containers: string[]
    containerName?: string
    orgId?: string
    clusterId?: string
    clusterName?: string
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
      orgId: opts.orgId,
      clusterId: opts.clusterId,
      clusterName: opts.clusterName,
    })
  }
}

export function useOpenWorkloadLogs() {
  const { addTab } = useDock()

  return (opts: {
    namespace: string
    workloadKind: string
    workloadName: string
    orgId?: string
    clusterId?: string
    clusterName?: string
  }) => {
    addTab({
      type: 'workload-logs',
      title: `${opts.workloadName} logs`,
      namespace: opts.namespace,
      workloadKind: opts.workloadKind,
      workloadName: opts.workloadName,
      orgId: opts.orgId,
      clusterId: opts.clusterId,
      clusterName: opts.clusterName,
    })
  }
}

export function useOpenNodeTerminal() {
  const { addTab } = useDock()

  return (opts: {
    nodeName: string
    orgId?: string
    clusterId?: string
    clusterName?: string
  }) => {
    addTab({
      type: 'node-terminal',
      title: `node: ${opts.nodeName}`,
      nodeName: opts.nodeName,
      orgId: opts.orgId,
      clusterId: opts.clusterId,
      clusterName: opts.clusterName,
    })
  }
}

export function useOpenLocalTerminal() {
  const { addTab } = useDock()

  return (opts?: { initialCommand?: string; title?: string }) => {
    addTab({
      type: 'local-terminal',
      title: opts?.title || 'Terminal',
      initialCommand: opts?.initialCommand,
    })
  }
}
