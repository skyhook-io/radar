import { useEffect, useLayoutEffect, useRef, useCallback, useState } from 'react'
import { Terminal as XTerm } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import '@xterm/xterm/css/xterm.css'
import { RefreshCw } from 'lucide-react'
import { clsx } from 'clsx'

export interface LocalTerminalTabProps {
  isActive?: boolean
  /** Returns the WebSocket URL for the local terminal session */
  createSession: () => Promise<{ wsUrl: string }>
}

export function LocalTerminalTab({
  isActive = true,
  createSession,
}: LocalTerminalTabProps) {
  const terminalRef = useRef<HTMLDivElement>(null)
  const xtermRef = useRef<XTerm | null>(null)
  const fitAddonRef = useRef<FitAddon | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const cleanupRef = useRef<(() => void) | undefined>(undefined)
  const cancelledRef = useRef(false)
  const createSessionRef = useRef(createSession)
  useLayoutEffect(() => { createSessionRef.current = createSession }, [createSession])
  const [isConnected, setIsConnected] = useState(false)
  const [isConnecting, setIsConnecting] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const connect = useCallback(() => {
    if (!terminalRef.current) return

    cancelledRef.current = false
    setIsConnecting(true)
    setError(null)

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

    createSessionRef.current()
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

        // Debounced resize
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
            if (fitAddonRef.current && xtermRef.current) {
              const dims = fitAddonRef.current.proposeDimensions()
              if (dims) xtermRef.current.resize(dims.cols, dims.rows)
              if (ws.readyState === WebSocket.OPEN) {
                ws.send(JSON.stringify({ type: 'resize', rows: xtermRef.current.rows, cols: xtermRef.current.cols }))
              }
            }
          }, 100)
        })
        if (terminalRef.current) {
          resizeObserver.observe(terminalRef.current)
          cleanupRef.current = () => resizeObserver.disconnect()
        } else {
          resizeObserver.disconnect()
        }
      })
      .catch((err) => {
        setError(err instanceof Error ? err.message : 'Failed to connect')
        setIsConnecting(false)
      })
  }, [])

  useEffect(() => {
    connect()
    return () => {
      cancelledRef.current = true
      cleanupRef.current?.()
      wsRef.current?.close()
      xtermRef.current?.dispose()
    }
  }, [connect])

  // Refit when tab becomes active
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
        <span className="text-xs text-theme-text-tertiary">Local Terminal</span>

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

      {/* Terminal or error */}
      {error ? (
        <div key="error" className="absolute top-8 left-0 right-0 bottom-0 flex flex-col items-center justify-center p-4 text-center bg-slate-900">
          <div className="text-red-400 mb-2 text-sm">Failed to connect</div>
          <div className="text-xs text-theme-text-disabled mb-3">{error}</div>
          <button
            onClick={connect}
            className="flex items-center gap-2 px-3 py-1.5 btn-brand text-xs rounded"
          >
            <RefreshCw className="w-3 h-3" />
            Retry
          </button>
        </div>
      ) : (
        <div key="terminal" ref={terminalRef} className="absolute top-8 left-0 right-0 bottom-0 bg-[#0f172a] [&_.xterm-viewport]:!bg-[#0f172a]" />
      )}
    </div>
  )
}
