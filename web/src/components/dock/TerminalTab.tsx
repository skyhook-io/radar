import { useEffect, useRef, useCallback, useState } from 'react'
import { Terminal as XTerm } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import '@xterm/xterm/css/xterm.css'
import { RefreshCw, ChevronDown, Bug } from 'lucide-react'
import { clsx } from 'clsx'
import { Tooltip } from '../ui/Tooltip'

interface TerminalTabProps {
  namespace: string
  podName: string
  containerName: string
  containers: string[]
  isActive?: boolean
}

interface TerminalMessage {
  type: 'input' | 'resize' | 'output' | 'error'
  data?: string
  errorType?: 'shell_not_found' | 'exec_error'
  rows?: number
  cols?: number
}

export function TerminalTab({
  namespace,
  podName,
  containerName,
  containers,
  isActive = true,
}: TerminalTabProps) {
  const terminalRef = useRef<HTMLDivElement>(null)
  const xtermRef = useRef<XTerm | null>(null)
  const fitAddonRef = useRef<FitAddon | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const [isConnected, setIsConnected] = useState(false)
  const [isConnecting, setIsConnecting] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [errorType, setErrorType] = useState<string | null>(null)
  const [isCreatingDebug, setIsCreatingDebug] = useState(false)
  const [selectedContainer, setSelectedContainer] = useState(containerName)

  const connect = useCallback(() => {
    if (!terminalRef.current) return

    setIsConnecting(true)
    setError(null)
    setErrorType(null)

    // Clean up existing terminal
    if (xtermRef.current) {
      xtermRef.current.dispose()
    }
    if (wsRef.current) {
      wsRef.current.close()
    }

    // Create terminal
    const xterm = new XTerm({
      cursorBlink: true,
      fontFamily: 'JetBrains Mono, Menlo, Monaco, monospace',
      fontSize: 13,
      lineHeight: 1.2,
      theme: {
        background: '#0f172a', // slate-900
        foreground: '#e2e8f0', // slate-200
        cursor: '#60a5fa', // blue-400
        cursorAccent: '#0f172a',
        selectionBackground: '#3b82f680', // blue-500/50
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

    // Connect WebSocket
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const wsUrl = `${protocol}//${window.location.host}/api/pods/${namespace}/${podName}/exec?container=${selectedContainer}`

    const ws = new WebSocket(wsUrl)
    wsRef.current = ws

    // Fit terminal to container and notify backend of new dimensions
    const doFit = () => {
      const dims = fitAddon.proposeDimensions()
      if (dims) {
        xterm.resize(dims.cols, dims.rows)
      }
      if (ws.readyState === WebSocket.OPEN) {
        const msg: TerminalMessage = {
          type: 'resize',
          rows: xterm.rows,
          cols: xterm.cols,
        }
        ws.send(JSON.stringify(msg))
      }
    }

    // Delay initial fit to ensure container is sized
    requestAnimationFrame(() => {
      doFit()
      // Second fit after layout settles
      setTimeout(doFit, 100)
    })

    ws.onopen = () => {
      setIsConnected(true)
      setIsConnecting(false)

      // Fit to container and send actual dimensions (not xterm defaults)
      doFit()
      xterm.focus()
    }

    ws.onmessage = (event) => {
      try {
        const msg: TerminalMessage = JSON.parse(event.data)
        if (msg.type === 'output' && msg.data) {
          xterm.write(msg.data)
        } else if (msg.type === 'error' && msg.data) {
          setError(msg.data)
          setErrorType(msg.errorType || 'exec_error')
          setIsConnected(false)
        }
      } catch {
        // Raw data fallback
        xterm.write(event.data)
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

    // Handle input
    xterm.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) {
        const msg: TerminalMessage = { type: 'input', data }
        ws.send(JSON.stringify(msg))
      }
    })

    // Handle resize with debounce to prevent infinite loops
    let resizeTimeout: ReturnType<typeof setTimeout> | null = null
    let lastWidth = 0
    let lastHeight = 0
    const resizeObserver = new ResizeObserver((entries) => {
      const entry = entries[0]
      if (!entry) return

      // Only process if size actually changed significantly
      const { width, height } = entry.contentRect
      if (Math.abs(width - lastWidth) < 5 && Math.abs(height - lastHeight) < 5) return
      lastWidth = width
      lastHeight = height

      if (resizeTimeout) clearTimeout(resizeTimeout)
      resizeTimeout = setTimeout(() => {
        if (fitAddonRef.current && xtermRef.current) {
          const dims = fitAddonRef.current.proposeDimensions()
          if (dims) {
            xtermRef.current.resize(dims.cols, dims.rows)
          }
          if (ws.readyState === WebSocket.OPEN) {
            const msg: TerminalMessage = {
              type: 'resize',
              rows: xtermRef.current.rows,
              cols: xtermRef.current.cols,
            }
            ws.send(JSON.stringify(msg))
          }
        }
      }, 100)
    })
    resizeObserver.observe(terminalRef.current)

    return () => {
      resizeObserver.disconnect()
    }
  }, [namespace, podName, selectedContainer])

  // Connect on mount and when container changes
  useEffect(() => {
    const cleanup = connect()
    return () => {
      cleanup?.()
      wsRef.current?.close()
      xtermRef.current?.dispose()
    }
  }, [connect])

  // Reconnect when container changes
  const handleContainerChange = useCallback((container: string) => {
    setSelectedContainer(container)
  }, [])

  // Create debug container for distroless pods
  const handleCreateDebugContainer = useCallback(async () => {
    setIsCreatingDebug(true)
    try {
      const response = await fetch(`/api/pods/${namespace}/${podName}/debug`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          targetContainer: selectedContainer,
          image: 'busybox:latest',
        }),
      })

      if (!response.ok) {
        const err = await response.json().catch(() => ({ error: 'Unknown error' }))
        throw new Error(err.error || `HTTP ${response.status}`)
      }

      const result = await response.json()

      // Clear error and reconnect with the debug container
      setError(null)
      setErrorType(null)
      setSelectedContainer(result.containerName)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create debug container')
      setErrorType('debug_error')
    } finally {
      setIsCreatingDebug(false)
    }
  }, [namespace, podName, selectedContainer])

  // Refit terminal when tab becomes active (might have been resized while hidden)
  useEffect(() => {
    if (isActive && fitAddonRef.current && xtermRef.current) {
      const dims = fitAddonRef.current.proposeDimensions()
      if (dims) {
        xtermRef.current.resize(dims.cols, dims.rows)
      }
      xtermRef.current.focus()
    }
  }, [isActive])

  return (
    <div className="relative h-full w-full bg-theme-base overflow-hidden">
      {/* Mini toolbar */}
      <div className="h-8 flex items-center gap-2 px-2 bg-theme-surface/50 border-b border-theme-border/50">
        <Tooltip
          content={isConnected ? 'Connected to pod' : isConnecting ? 'Connecting...' : 'Disconnected - click Reconnect'}
          position="bottom"
        >
          <span
            className={clsx(
              'w-2 h-2 rounded-full cursor-help',
              isConnected ? 'bg-green-500' : isConnecting ? 'bg-yellow-500 animate-pulse' : 'bg-red-500'
            )}
          />
        </Tooltip>
        <span className="text-xs text-theme-text-tertiary">
          {podName}
        </span>

        {containers.length > 1 && (
          <div className="relative">
            <select
              value={selectedContainer}
              onChange={(e) => handleContainerChange(e.target.value)}
              className="appearance-none bg-theme-elevated text-xs text-theme-text-primary px-2 py-0.5 pr-5 rounded border border-theme-border-light focus:outline-none focus:ring-1 focus:ring-blue-500"
            >
              {containers.map((c) => (
                <option key={c} value={c}>
                  {c}
                </option>
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

      {/* Terminal or error — keys force React to unmount/remount so xterm canvas is removed */}
      {error ? (
        <div key="error" className="absolute top-8 left-0 right-0 bottom-0 flex flex-col items-center justify-center p-4 text-center bg-slate-900">
          {errorType === 'shell_not_found' ? (
            <>
              <div className="text-amber-400 mb-2 text-sm">Shell not available</div>
              <div className="text-xs text-theme-text-tertiary mb-4 max-w-md">
                This container doesn't have a shell (/bin/sh). This is common with distroless
                or minimal container images. You can create a debug container to troubleshoot.
              </div>
              <div className="flex gap-2">
                <button
                  onClick={handleCreateDebugContainer}
                  disabled={isCreatingDebug}
                  className="flex items-center gap-2 px-3 py-1.5 bg-blue-600 text-white text-xs rounded hover:bg-blue-700 disabled:opacity-50"
                >
                  {isCreatingDebug ? (
                    <>
                      <RefreshCw className="w-3 h-3 animate-spin" />
                      Creating debug container...
                    </>
                  ) : (
                    <>
                      <Bug className="w-3 h-3" />
                      Start debug container
                    </>
                  )}
                </button>
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
                className="flex items-center gap-2 px-3 py-1.5 bg-blue-600 text-white text-xs rounded hover:bg-blue-700"
              >
                <RefreshCw className="w-3 h-3" />
                Retry
              </button>
            </>
          )}
        </div>
      ) : (
        <div key="terminal" ref={terminalRef} className="absolute top-8 left-0 right-0 bottom-0" />
      )}
    </div>
  )
}
