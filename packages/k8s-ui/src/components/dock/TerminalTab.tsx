import { useEffect, useLayoutEffect, useRef, useCallback, useState, type ReactNode } from 'react'
import { Terminal as XTerm } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import '@xterm/xterm/css/xterm.css'
import { RefreshCw, ChevronDown, Bug } from 'lucide-react'
import { clsx } from 'clsx'
import { setupTerminalClipboard, copyTerminalSelection } from './terminalClipboard'
import { TerminalClipboardToolbar } from './TerminalClipboardToolbar'
import { showApiError, showApiNotice } from '../ui/Toast'
import { useMultilinePasteConfirm } from './useMultilinePasteConfirm'
import { attachTerminalKeys } from './terminalKeys'
import { useTerminalSearch } from './useTerminalSearch'
import { TerminalSearchBar, TerminalSearchButton } from './TerminalSearchBar'
import { toneTextClass } from '../ui/status-tone'
import { useTerminalCopyOnSelect } from './terminalPreferences'

export interface TerminalTabProps {
  namespace: string
  podName: string
  containerName: string
  containers: string[]
  isActive?: boolean
  /** Returns the WebSocket URL to connect to for a given container. */
  createSession: (containerName: string, podUID?: string) => Promise<{ wsUrl: string }>
  /** Optional: creates a debug (ephemeral) container. If omitted, the debug button is hidden. */
  createDebugContainer?: (targetContainer: string) => Promise<{ containerName: string }>
  podDetailsLink?: ReactNode
  renderDebugAction?: (targetContainer: string, podUID: string, switchContainer?: (name: string) => void) => ReactNode
  /** Optional: a short line in the toolbar describing the session. */
  note?: string
  /** Executable supplied by the host session callback; pins its target container. */
  command?: string
}

export function TerminalTab({
  namespace,
  podName,
  containerName,
  containers,
  isActive = true,
  createSession,
  createDebugContainer,
  renderDebugAction,
  podDetailsLink,
  note,
  command,
}: TerminalTabProps) {
  const terminalRef = useRef<HTMLDivElement>(null)
  const xtermRef = useRef<XTerm | null>(null)
  const fitAddonRef = useRef<FitAddon | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const reviewedPodUID = useRef<string | undefined>(undefined)
  // Cleanup ref holds the ResizeObserver disconnect from an async setup
  const cleanupRef = useRef<(() => void) | undefined>(undefined)
  // Prevents stale .then() callbacks from running after unmount or reconnect
  const cancelledRef = useRef(false)
  // Stable refs for callbacks — avoids effect re-runs when consumers pass unstable functions
  const createSessionRef = useRef(createSession)
  const createDebugContainerRef = useRef(createDebugContainer)
  useLayoutEffect(() => { createSessionRef.current = createSession }, [createSession])
  useLayoutEffect(() => { createDebugContainerRef.current = createDebugContainer }, [createDebugContainer])
  const [isConnected, setIsConnected] = useState(false)
  const [isConnecting, setIsConnecting] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [errorType, setErrorType] = useState<string | null>(null)
  const [failedExecutable, setFailedExecutable] = useState<string>()
  const [failedPodUID, setFailedPodUID] = useState<string>()
  const [isCreatingDebug, setIsCreatingDebug] = useState(false)
  const [selectedContainer, setSelectedContainer] = useState(containerName)
  const [hasSelection, setHasSelection] = useState(false)
  const { confirmPaste, pasteDialog } = useMultilinePasteConfirm()
  const search = useTerminalSearch(isActive)
  const { getState: copyOnSelect } = useTerminalCopyOnSelect()
  const { attach: attachSearch, open: openSearch, restoreFocus } = search

  const handleCopy = useCallback(() => {
    if (xtermRef.current) copyTerminalSelection(xtermRef.current)
  }, [])

  const connect = useCallback(() => {
    if (!terminalRef.current) return

    cancelledRef.current = false
    setFailedPodUID(undefined)
    setFailedExecutable(undefined)
    setIsConnecting(true)
    setError(null)
    setErrorType(null)

    // Clean up existing terminal and connection
    cleanupRef.current?.()
    xtermRef.current?.dispose()
    wsRef.current?.close()

    const xterm = new XTerm({
      cursorBlink: true,
      fontFamily: 'JetBrains Mono, Menlo, Monaco, monospace',
      fontSize: 13,
      lineHeight: 1.2,
      // Search highlights use xterm's decoration API, which is still marked proposed.
      allowProposedApi: true,
      theme: {
        background: '#0f172a',
        foreground: '#e2e8f0',
        cursor: '#60a5fa',
        cursorAccent: '#0f172a',
        selectionBackground: '#3b82f680',
        black: '#1e293b',
        red: '#f87171',
        green: '#4ade80',
        yellow: '#facc15',
        blue: '#60a5fa',
        magenta: '#c084fc',
        cyan: '#22d3ee',
        white: '#f1f5f9',
        brightBlack: '#475569',
        brightRed: '#fca5a5',
        brightGreen: '#86efac',
        brightYellow: '#fde047',
        brightBlue: '#93c5fd',
        brightMagenta: '#d8b4fe',
        brightCyan: '#67e8f9',
        brightWhite: '#f8fafc',
      },
    })

    const fitAddon = new FitAddon()
    const webLinksAddon = new WebLinksAddon()
    xterm.loadAddon(fitAddon)
    xterm.loadAddon(webLinksAddon)
    xterm.open(terminalRef.current)

    xtermRef.current = xterm
    fitAddonRef.current = fitAddon

    setHasSelection(false)
    const disposeClipboard = setupTerminalClipboard(xterm, terminalRef.current, {
      confirmPaste,
      onSelectionChange: setHasSelection,
      copyOnSelect,
      onCopyError: () => showApiError('Could not copy the selection. Check clipboard access before pasting.'),
      onCopyPending: () => showApiNotice('Selection not copied. Account information is unavailable.', 'Wait for it to load, or switch to another tab or window and back to retry.', 'warning'),
    })
    const detachSearch = attachSearch(xterm)
    attachTerminalKeys(xterm, { onFind: openSearch })

    const doFit = (ws?: WebSocket) => {
      const dims = fitAddon.proposeDimensions()
      if (dims) xterm.resize(dims.cols, dims.rows)
      const conn = ws ?? wsRef.current
      if (conn?.readyState === WebSocket.OPEN) {
        conn.send(JSON.stringify({ type: 'resize', rows: xterm.rows, cols: xterm.cols }))
      }
    }

    requestAnimationFrame(() => {
      doFit()
      setTimeout(doFit, 100)
    })

    // Attach ResizeObserver synchronously — before createSession() resolves.
    // The dock's expand animation runs concurrently with session creation, so
    // a late-attached observer would miss its size changes and leave the
    // terminal stuck at whatever dimensions it had when the dock was still
    // collapsing. Debounced to coalesce animation frames.
    let resizeTimeout: ReturnType<typeof setTimeout> | null = null
    let lastWidth = 0
    let lastHeight = 0
    const resizeObserver = new ResizeObserver((entries) => {
      const entry = entries[0]
      if (!entry) return
      const { width, height } = entry.contentRect
      if (Math.abs(width - lastWidth) < 5 && Math.abs(height - lastHeight) < 5) return
      lastWidth = width
      lastHeight = height
      if (resizeTimeout) clearTimeout(resizeTimeout)
      resizeTimeout = setTimeout(() => {
        if (!fitAddonRef.current || !xtermRef.current) return
        const dims = fitAddonRef.current.proposeDimensions()
        if (dims) xtermRef.current.resize(dims.cols, dims.rows)
        const conn = wsRef.current
        if (conn?.readyState === WebSocket.OPEN) {
          conn.send(JSON.stringify({ type: 'resize', rows: xtermRef.current.rows, cols: xtermRef.current.cols }))
        }
      }, 100)
    })
    resizeObserver.observe(terminalRef.current)
    cleanupRef.current = () => {
      resizeObserver.disconnect()
      disposeClipboard()
      detachSearch()
    }

    const uid = reviewedPodUID.current
    const session = uid ? createSessionRef.current(selectedContainer, uid) : createSessionRef.current(selectedContainer)
    session
      .then(({ wsUrl }) => {
        if (cancelledRef.current) return
        const ws = new WebSocket(wsUrl)
        wsRef.current = ws

        ws.onopen = () => {
          setIsConnected(true)
          setIsConnecting(false)
          doFit(ws)
          restoreFocus()
        }

        let receivedOutput = false
        ws.onmessage = (event) => {
          let msg: Record<string, string> | null = null
          try {
            msg = JSON.parse(event.data as string) as Record<string, string>
          } catch {
            receivedOutput = true
            xterm.write(event.data as string)
            return
          }
          if (msg.type === 'output' && msg.data) {
            receivedOutput = true
            xterm.write(msg.data)
          } else if (msg.type === 'exit') {
            xterm.write('\r\n\x1b[2m[Process exited]\x1b[0m\r\n')
          } else if (msg.type === 'error' && msg.data) {
            // Once the server identifies the Pod, every retry in this tab stays on it,
            // including after a later UID-mismatch error that reports no identifier.
            if (msg.podUID) reviewedPodUID.current ??= msg.podUID
            if (receivedOutput && !command) {
              xterm.write(`\r\n${msg.data}\r\n`)
              setIsConnected(false)
              return
            }
            setFailedPodUID(msg.podUID)
            setFailedExecutable(msg.executable)
            setError(msg.data)
            // Support both camelCase (radar) and snake_case (conduit) error type field
            setErrorType(msg.errorType ?? msg.error_type ?? 'exec_error')
            setIsConnected(false)
          }
        }

        ws.onerror = () => {
          setError((prev) => prev || 'Connection error')
          setIsConnected(false)
          setIsConnecting(false)
        }

        ws.onclose = () => {
          setIsConnected(false)
          setIsConnecting(false)
          xterm.write('\r\n\x1b[31mConnection closed\x1b[0m\r\n')
        }

        xterm.onData((data: string) => {
          if (ws.readyState === WebSocket.OPEN) {
            ws.send(JSON.stringify({ type: 'input', data }))
          }
        })
      })
      .catch((err) => {
        setError(err instanceof Error ? err.message : 'Failed to connect')
        setErrorType('exec_error')
        setIsConnecting(false)
      })
  }, [namespace, podName, selectedContainer, command, attachSearch, openSearch, restoreFocus])

  useEffect(() => {
    connect()
    return () => {
      cancelledRef.current = true
      cleanupRef.current?.()
      wsRef.current?.close()
      xtermRef.current?.dispose()
    }
  }, [connect])

  const handleContainerChange = useCallback((container: string) => {
    setSelectedContainer(container)
  }, [])

  const handleCreateDebugContainer = useCallback(async () => {
    if (!createDebugContainerRef.current) return
    setIsCreatingDebug(true)
    try {
      const { containerName: newContainer } = await createDebugContainerRef.current(selectedContainer)
      setError(null)
      setErrorType(null)
      setSelectedContainer(newContainer)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create debug container')
      setErrorType('exec_error')
    } finally {
      setIsCreatingDebug(false)
    }
  }, [selectedContainer])

  // Refit when tab becomes active (may have been resized while hidden)
  useEffect(() => {
    if (isActive && fitAddonRef.current && xtermRef.current) {
      const dims = fitAddonRef.current.proposeDimensions()
      if (dims) xtermRef.current.resize(dims.cols, dims.rows)
      restoreFocus()
    }
  }, [isActive, restoreFocus])

  return (
    <div className="relative h-full w-full bg-theme-base overflow-hidden">
      {/* Mini toolbar */}
      <div className="h-8 flex items-center gap-2 px-2 bg-theme-surface/50 border-b border-theme-border/50">
        <span
          title={isConnected ? 'Connected' : isConnecting ? 'Connecting...' : 'Disconnected'}
          className={clsx(
            'w-2 h-2 rounded-full',
            isConnected ? 'bg-green-500' : isConnecting ? 'bg-yellow-500 animate-pulse' : 'bg-red-500'
          )}
        />
        <span className="text-xs text-theme-text-tertiary">{podName}</span>
        {note && <span className="truncate text-xs text-theme-text-secondary">{note}</span>}

        {!command && containers.length > 1 && (
          <div className="relative">
            <select aria-label="Terminal container"
              value={selectedContainer}
              onChange={(e) => handleContainerChange(e.target.value)}
              className="appearance-none bg-theme-elevated text-xs text-theme-text-primary px-2 py-0.5 pr-5 rounded border border-theme-border-light focus:outline-none focus:ring-1 focus:ring-blue-500"
            >
              {containers.map((c) => (
                <option key={c} value={c}>{c}</option>
              ))}
            </select>
            <ChevronDown className="absolute right-1 top-1/2 -translate-y-1/2 w-3 h-3 text-theme-text-tertiary pointer-events-none" />
          </div>
        )}

        {!isConnected && !isConnecting && errorType !== 'pod_replaced' && (command || errorType !== 'shell_not_found') && (
          <button
            onClick={connect}
            className="flex items-center gap-1 px-2 py-0.5 text-xs text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
          >
            <RefreshCw className="w-3 h-3" />
            Reconnect
          </button>
        )}

        {!error && (
          <TerminalClipboardToolbar
            hasSelection={hasSelection}
            onCopy={handleCopy}
            leading={<TerminalSearchButton onClick={openSearch} />}
          />
        )}
      </div>

      <div ref={terminalRef} className="absolute top-8 left-0 right-0 bottom-0 bg-[#0f172a] [&_.xterm-viewport]:!bg-[#0f172a]" />
      {error && (
        <div key="error" className="absolute top-8 left-0 right-0 bottom-0 flex flex-col items-center justify-center-safe overflow-y-auto p-4 text-center bg-theme-base">
          {errorType === 'pod_replaced' ? <>
            <div className="mb-2 text-sm font-medium text-theme-text-primary">The original Pod was replaced</div>
            <p className="mb-3 text-xs text-theme-text-secondary">This terminal belongs to the previous Pod. Review the current Pod before opening a new terminal.</p>
            {podDetailsLink || <p className="text-xs text-theme-text-secondary">Close this tab and open the Pod in Resources.</p>}
          </> : errorType === 'shell_not_found' ? (
            <>
              <div className="text-warning-text mb-2 break-words text-sm font-medium">{command ? 'Command not available' : <>Couldn’t start {failedExecutable ? <code>{failedExecutable}</code> : 'a shell'} in <code>{selectedContainer}</code></>}</div>
              {command ? (
                <div className="text-xs text-theme-text-tertiary mb-4 max-w-md">
                  Container <span className="font-mono text-theme-text-secondary">{selectedContainer}</span> could not start <span className="font-mono text-theme-text-secondary">{command}</span>. Check that the executable is installed in this container.
                </div>
              ) : <div className="text-xs text-theme-text-tertiary mb-4 max-w-md">
                This container image may have no shell.
              </div>}
              <div className="flex w-full min-w-0 max-w-md flex-wrap items-center justify-center gap-2">
                {!command && failedPodUID && renderDebugAction?.(selectedContainer, failedPodUID, handleContainerChange)}
                {!command && !renderDebugAction && createDebugContainerRef.current && (
                  <button
                    onClick={handleCreateDebugContainer}
                    disabled={isCreatingDebug}
                    className="flex items-center gap-2 px-3 py-1.5 btn-brand text-xs rounded"
                  >
                    {isCreatingDebug ? (
                      <><RefreshCw className="w-3 h-3 animate-spin" />Creating debug container...</>
                    ) : (
                      <><Bug className="w-3 h-3" />Start debug container</>
                    )}
                  </button>
                )}
                {command && <button
                  onClick={connect}
                  className="flex items-center gap-2 px-3 py-1.5 bg-theme-elevated text-theme-text-primary text-xs rounded hover:bg-theme-hover"
                >
                  <RefreshCw className="w-3 h-3" />
                  Retry
                </button>}
              </div>
              {!command && <button
                type="button" onClick={connect}
                className="mt-3 text-xs text-theme-text-secondary underline underline-offset-2 hover:text-theme-text-primary"
              >Retry terminal</button>}
            </>
          ) : (
            <>
              <div className={clsx(toneTextClass('unhealthy'), 'mb-2 text-sm')}>Failed to connect</div>
              <div className="text-xs text-theme-text-secondary mb-3">{error}</div>
              {reviewedPodUID.current && podDetailsLink}
              <button
                onClick={connect}
                className="flex items-center gap-2 px-3 py-1.5 btn-brand text-xs rounded"
              >
                <RefreshCw className="w-3 h-3" />
                Retry
              </button>
            </>
          )}
        </div>
      )}
      {!error && <TerminalSearchBar search={search} />}
      {pasteDialog}
    </div>
  )
}
