import { useEffect, useLayoutEffect, useRef, useCallback, useState } from 'react'
import { Terminal as XTerm } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import '@xterm/xterm/css/xterm.css'
import { RefreshCw, ChevronDown, Bug } from 'lucide-react'
import { clsx } from 'clsx'

export interface TerminalTabProps {
  namespace: string
  podName: string
  containerName: string
  containers: string[]
  isActive?: boolean
  /** Returns the WebSocket URL to connect to for a given container. */
  createSession: (containerName: string) => Promise<{ wsUrl: string }>
  /** Optional: creates a debug (ephemeral) container. If omitted, the debug button is hidden. */
  createDebugContainer?: (targetContainer: string) => Promise<{ containerName: string }>
}

export function TerminalTab({
  namespace,
  podName,
  containerName,
  containers,
  isActive = true,
  createSession,
  createDebugContainer,
}: TerminalTabProps) {
  const terminalRef = useRef<HTMLDivElement>(null)
  const xtermRef = useRef<XTerm | null>(null)
  const fitAddonRef = useRef<FitAddon | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
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
  const [isCreatingDebug, setIsCreatingDebug] = useState(false)
  const [selectedContainer, setSelectedContainer] = useState(containerName)

  const connect = useCallback(() => {
    if (!terminalRef.current) return

    cancelledRef.current = false
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
    cleanupRef.current = () => resizeObserver.disconnect()

    createSessionRef.current(selectedContainer)
      .then(({ wsUrl }) => {
        if (cancelledRef.current) return
        const ws = new WebSocket(wsUrl)
        wsRef.current = ws

        ws.onopen = () => {
          setIsConnected(true)
          setIsConnecting(false)
          doFit(ws)
          xterm.focus()
        }

        ws.onmessage = (event) => {
          let msg: Record<string, string> | null = null
          try {
            msg = JSON.parse(event.data as string) as Record<string, string>
          } catch {
            xterm.write(event.data as string)
            return
          }
          if (msg.type === 'output' && msg.data) {
            xterm.write(msg.data)
          } else if (msg.type === 'exit') {
            xterm.write('\r\n\x1b[2m[Process exited]\x1b[0m\r\n')
          } else if (msg.type === 'error' && msg.data) {
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
  }, [namespace, podName, selectedContainer])

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
      xtermRef.current.focus()
    }
  }, [isActive])

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

        {containers.length > 1 && (
          <div className="relative">
            <select
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

        {!isConnected && !isConnecting && (
          <button
            onClick={connect}
            className="flex items-center gap-1 px-2 py-0.5 text-xs text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
          >
            <RefreshCw className="w-3 h-3" />
            Reconnect
          </button>
        )}
      </div>

      {/* Terminal or error — key forces xterm canvas unmount/remount on toggle */}
      {error ? (
        <div key="error" className="absolute top-8 left-0 right-0 bottom-0 flex flex-col items-center justify-center p-4 text-center bg-slate-900">
          {errorType === 'shell_not_found' ? (
            <>
              <div className="text-amber-400 mb-2 text-sm">Shell not available</div>
              <div className="text-xs text-theme-text-tertiary mb-4 max-w-md">
                This container doesn&apos;t have a shell (/bin/sh). This is common with distroless
                or minimal container images. You can create a debug container to troubleshoot.
              </div>
              <div className="flex gap-2">
                {createDebugContainerRef.current && (
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
                <button
                  onClick={connect}
                  className="flex items-center gap-2 px-3 py-1.5 bg-theme-elevated text-theme-text-primary text-xs rounded hover:bg-theme-hover"
                >
                  <RefreshCw className="w-3 h-3" />
                  Retry
                </button>
              </div>
            </>
          ) : (
            <>
              <div className="text-red-400 mb-2 text-sm">Failed to connect</div>
              <div className="text-xs text-theme-text-disabled mb-3">{error}</div>
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
      ) : (
        <div key="terminal" ref={terminalRef} className="absolute top-8 left-0 right-0 bottom-0 bg-[#0f172a] [&_.xterm-viewport]:!bg-[#0f172a]" />
      )}
    </div>
  )
}
