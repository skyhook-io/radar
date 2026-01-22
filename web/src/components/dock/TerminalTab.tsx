import { useEffect, useRef, useCallback, useState } from 'react'
import { Terminal as XTerm } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import '@xterm/xterm/css/xterm.css'
import { RefreshCw, ChevronDown } from 'lucide-react'
import { clsx } from 'clsx'

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
  const [selectedContainer, setSelectedContainer] = useState(containerName)

  const connect = useCallback(() => {
    if (!terminalRef.current) return

    setIsConnecting(true)
    setError(null)

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

    // Delay fit to ensure container is sized
    // Use proposeDimensions + 1 workaround for better space utilization
    const doFit = () => {
      const dims = fitAddon.proposeDimensions()
      if (dims) {
        xterm.resize(dims.cols, dims.rows)
      }
    }

    requestAnimationFrame(() => {
      doFit()
      // Second fit after layout settles
      setTimeout(doFit, 100)
    })

    xtermRef.current = xterm
    fitAddonRef.current = fitAddon

    // Connect WebSocket
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const wsUrl = `${protocol}//${window.location.host}/api/pods/${namespace}/${podName}/exec?container=${selectedContainer}`

    const ws = new WebSocket(wsUrl)
    wsRef.current = ws

    ws.onopen = () => {
      setIsConnected(true)
      setIsConnecting(false)
      xterm.focus()

      // Send initial size
      const msg: TerminalMessage = {
        type: 'resize',
        rows: xterm.rows,
        cols: xterm.cols,
      }
      ws.send(JSON.stringify(msg))
    }

    ws.onmessage = (event) => {
      try {
        const msg: TerminalMessage = JSON.parse(event.data)
        if (msg.type === 'output' && msg.data) {
          xterm.write(msg.data)
        } else if (msg.type === 'error' && msg.data) {
          setError(msg.data)
          setIsConnected(false)
        }
      } catch {
        // Raw data fallback
        xterm.write(event.data)
      }
    }

    ws.onerror = () => {
      setError('Connection error')
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
    <div className="relative h-full w-full bg-slate-900 overflow-hidden">
      {/* Mini toolbar */}
      <div className="h-8 flex items-center gap-2 px-2 bg-slate-800/50 border-b border-slate-700/50">
        <span
          className={clsx(
            'w-2 h-2 rounded-full',
            isConnected ? 'bg-green-500' : isConnecting ? 'bg-yellow-500 animate-pulse' : 'bg-red-500'
          )}
        />
        <span className="text-xs text-slate-400">
          {podName}
        </span>

        {containers.length > 1 && (
          <div className="relative">
            <select
              value={selectedContainer}
              onChange={(e) => handleContainerChange(e.target.value)}
              className="appearance-none bg-slate-700 text-xs text-white px-2 py-0.5 pr-5 rounded border border-slate-600 focus:outline-none focus:ring-1 focus:ring-blue-500"
            >
              {containers.map((c) => (
                <option key={c} value={c}>
                  {c}
                </option>
              ))}
            </select>
            <ChevronDown className="absolute right-1 top-1/2 -translate-y-1/2 w-3 h-3 text-slate-400 pointer-events-none" />
          </div>
        )}

        {!isConnected && !isConnecting && (
          <button
            onClick={connect}
            className="flex items-center gap-1 px-2 py-0.5 text-xs text-slate-400 hover:text-white hover:bg-slate-700 rounded"
          >
            <RefreshCw className="w-3 h-3" />
            Reconnect
          </button>
        )}
      </div>

      {/* Terminal or error */}
      {error ? (
        <div className="absolute top-8 left-0 right-0 bottom-0 flex flex-col items-center justify-center p-4 text-center">
          <div className="text-red-400 mb-2 text-sm">Failed to connect</div>
          <div className="text-xs text-slate-500 mb-3">{error}</div>
          <button
            onClick={connect}
            className="flex items-center gap-2 px-3 py-1.5 bg-blue-600 text-white text-xs rounded hover:bg-blue-700"
          >
            <RefreshCw className="w-3 h-3" />
            Retry
          </button>
        </div>
      ) : (
        <div ref={terminalRef} className="absolute top-8 left-0 right-0 bottom-0" />
      )}
    </div>
  )
}
