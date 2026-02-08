import { useState, useRef, useEffect } from 'react'
import { Plug, ChevronDown, Loader2, Globe, Monitor, Copy, Check, X, Terminal } from 'lucide-react'
import { clsx } from 'clsx'
import { useAvailablePorts, useClusterInfo, AvailablePort } from '../../api/client'
import { useStartPortForward } from './PortForwardManager'

interface PortForwardButtonProps {
  type: 'pod' | 'service'
  namespace: string
  name: string
  // For service port forwarding
  serviceName?: string
  className?: string
}

interface KubectlDialogInfo {
  type: 'pod' | 'service'
  namespace: string
  name: string
  port: number
}

function buildKubectlCommand(type: 'pod' | 'service', namespace: string, name: string, localPort: number, remotePort: number) {
  const resource = type === 'pod' ? `pod/${name}` : `svc/${name}`
  const portArg = localPort === remotePort ? `${remotePort}` : `${localPort}:${remotePort}`
  return `kubectl port-forward -n ${namespace} ${resource} ${portArg}`
}

function KubectlCommandDialog({
  info,
  onClose,
}: {
  info: KubectlDialogInfo
  onClose: () => void
}) {
  const [copied, setCopied] = useState(false)
  const [copyFallback, setCopyFallback] = useState(false)
  const [localPort, setLocalPort] = useState(info.port)
  const commandRef = useRef<HTMLElement>(null)
  const dialogRef = useRef<HTMLDivElement>(null)

  const command = buildKubectlCommand(info.type, info.namespace, info.name, localPort, info.port)

  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(command)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch {
      // Clipboard API unavailable (e.g. non-HTTPS context) — select text for manual copy
      if (commandRef.current) {
        const range = document.createRange()
        range.selectNodeContents(commandRef.current)
        const sel = window.getSelection()
        sel?.removeAllRanges()
        sel?.addRange(range)
      }
      setCopyFallback(true)
      setTimeout(() => setCopyFallback(false), 3000)
    }
  }

  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [onClose])

  useEffect(() => {
    dialogRef.current?.focus()
  }, [])

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      <div className="absolute inset-0 bg-black/60 backdrop-blur-sm" onClick={onClose} />
      <div
        ref={dialogRef}
        tabIndex={-1}
        className="relative bg-theme-surface border border-theme-border rounded-lg shadow-2xl max-w-lg w-full mx-4 outline-none"
      >
        <div className="flex items-center justify-between p-4 border-b border-theme-border">
          <div className="flex items-center gap-2">
            <Terminal className="w-5 h-5 text-blue-400" />
            <h3 className="text-base font-semibold text-theme-text-primary">Port Forward</h3>
          </div>
          <button
            onClick={onClose}
            className="p-1 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
          >
            <X className="w-5 h-5" />
          </button>
        </div>

        <div className="p-4 space-y-3">
          <p className="text-sm text-theme-text-secondary">
            Radar is running in-cluster, so port forwarding must be run from your local terminal.
          </p>
          <div className="flex items-center gap-2 text-sm text-theme-text-secondary">
            <label htmlFor="local-port">Local port:</label>
            <input
              id="local-port"
              type="number"
              min={1}
              max={65535}
              value={localPort}
              onChange={(e) => {
                const val = Number(e.target.value)
                if (val >= 1 && val <= 65535) setLocalPort(val)
                else if (e.target.value === '') setLocalPort(info.port)
              }}
              className="w-20 bg-theme-base border border-theme-border rounded px-2 py-1 text-sm text-theme-text-primary font-mono text-center"
            />
          </div>
          <div className="flex items-center gap-2">
            <code ref={commandRef} className="flex-1 text-sm bg-theme-base rounded px-3 py-2 text-blue-400 font-mono select-all">
              {command}
            </code>
            <button
              onClick={handleCopy}
              className="shrink-0 px-3 py-2 bg-blue-600 hover:bg-blue-700 text-white text-sm rounded-lg transition-colors flex items-center gap-1.5"
            >
              {copied ? <Check className="w-4 h-4" /> : <Copy className="w-4 h-4" />}
              {copied ? 'Copied' : copyFallback ? 'Press Ctrl+C' : 'Copy'}
            </button>
          </div>
          <p className="text-xs text-theme-text-secondary">
            Requires kubectl and authentication to this cluster.
          </p>
        </div>
      </div>
    </div>
  )
}

export function PortForwardButton({
  type,
  namespace,
  name,
  serviceName,
  className,
}: PortForwardButtonProps) {
  const [isOpen, setIsOpen] = useState(false)
  const [dialogInfo, setDialogInfo] = useState<KubectlDialogInfo | null>(null)
  const [listenAddress, setListenAddress] = useState<'127.0.0.1' | '0.0.0.0'>('127.0.0.1')
  const dropdownRef = useRef<HTMLDivElement>(null)

  const { data: clusterInfo } = useClusterInfo()
  const { data, isLoading } = useAvailablePorts(type, namespace, name)
  const startPortForward = useStartPortForward()

  const ports = data?.ports || []
  const inCluster = clusterInfo?.inCluster ?? false
  const isPending = !inCluster && startPortForward.isPending
  const resourceName = type === 'service' ? (serviceName || name) : name

  // Close dropdown when clicking outside
  useEffect(() => {
    function handleClickOutside(event: MouseEvent) {
      if (dropdownRef.current && !dropdownRef.current.contains(event.target as Node)) {
        setIsOpen(false)
      }
    }
    document.addEventListener('mousedown', handleClickOutside)
    return () => document.removeEventListener('mousedown', handleClickOutside)
  }, [])

  const handlePortSelect = (port: AvailablePort) => {
    setIsOpen(false)
    if (inCluster) {
      setDialogInfo({ type, namespace, name: resourceName, port: port.port })
    } else {
      startPortForward.mutate({
        namespace,
        podName: type === 'pod' ? name : undefined,
        serviceName: type === 'service' ? (serviceName || name) : undefined,
        podPort: port.port,
        listenAddress,
      })
    }
  }

  function renderButton() {
    // If no ports available, show disabled button
    if (!isLoading && ports.length === 0) {
      return (
        <button
          disabled
          className={clsx(
            'flex items-center gap-2 px-3 py-2 bg-slate-700 text-white text-sm rounded-lg opacity-50 cursor-not-allowed',
            className
          )}
          title="No ports available"
        >
          <Plug className="w-4 h-4" />
          No Ports
        </button>
      )
    }

    // If only one port, forward directly on click (most common case)
    if (ports.length === 1) {
      return (
        <button
          onClick={() => handlePortSelect(ports[0])}
          disabled={isPending}
          className={clsx(
            'flex items-center gap-2 px-3 py-2 bg-slate-700 text-white text-sm rounded-lg hover:bg-slate-600 transition-colors disabled:opacity-50',
            className
          )}
          title={`Port forward to ${ports[0].port}`}
        >
          {isPending ? (
            <Loader2 className="w-4 h-4 animate-spin" />
          ) : (
            <Plug className="w-4 h-4" />
          )}
          Forward :{ports[0].port}
        </button>
      )
    }

    // Multiple ports - show dropdown
    return (
      <div className="relative" ref={dropdownRef}>
        <button
          onClick={() => setIsOpen(!isOpen)}
          disabled={isLoading || isPending}
          className={clsx(
            'flex items-center gap-2 px-3 py-2 bg-slate-700 text-white text-sm rounded-lg hover:bg-slate-600 transition-colors disabled:opacity-50',
            className
          )}
        >
          {isLoading || isPending ? (
            <Loader2 className="w-4 h-4 animate-spin" />
          ) : (
            <Plug className="w-4 h-4" />
          )}
          Port Forward
          <ChevronDown className={clsx('w-3 h-3 transition-transform', isOpen && 'rotate-180')} />
        </button>

        {isOpen && (
          <div className="absolute top-full left-0 mt-1 w-64 bg-slate-800 border border-slate-700 rounded-lg shadow-xl z-50 py-1">
            {/* Listen address toggle - only for local mode */}
            {!inCluster && (
              <div className="px-3 py-2 border-b border-slate-700">
                <div className="text-xs text-slate-500 mb-2">Listen on</div>
                <div className="flex gap-1">
                  <button
                    onClick={(e) => { e.stopPropagation(); setListenAddress('127.0.0.1') }}
                    className={clsx(
                      'flex-1 flex items-center justify-center gap-1.5 px-2 py-1.5 text-xs rounded transition-colors',
                      listenAddress === '127.0.0.1'
                        ? 'bg-blue-600 text-white'
                        : 'bg-slate-700 text-slate-400 hover:text-slate-200'
                    )}
                    title="Only accessible from this machine"
                  >
                    <Monitor className="w-3 h-3" />
                    localhost
                  </button>
                  <button
                    onClick={(e) => { e.stopPropagation(); setListenAddress('0.0.0.0') }}
                    className={clsx(
                      'flex-1 flex items-center justify-center gap-1.5 px-2 py-1.5 text-xs rounded transition-colors',
                      listenAddress === '0.0.0.0'
                        ? 'bg-amber-600 text-white'
                        : 'bg-slate-700 text-slate-400 hover:text-slate-200'
                    )}
                    title="Accessible from other machines on the network"
                  >
                    <Globe className="w-3 h-3" />
                    all interfaces
                  </button>
                </div>
              </div>
            )}
            <div className="px-2 py-1.5 text-xs text-slate-500 border-b border-slate-700">
              Select port to forward
            </div>
            {ports.map((port, i) => (
              <button
                key={i}
                onClick={() => handlePortSelect(port)}
                className="w-full px-3 py-2 text-left text-sm text-slate-200 hover:bg-slate-700 flex items-center justify-between"
              >
                <span className="flex items-center gap-2">
                  <code className="text-blue-400">{port.port}</code>
                  <span className="text-slate-500">/{port.protocol || 'TCP'}</span>
                </span>
                <span className="text-xs text-slate-500">
                  {port.name && <span className="mr-2">{port.name}</span>}
                  {port.containerName && <span className="text-slate-600">{port.containerName}</span>}
                </span>
              </button>
            ))}
          </div>
        )}
      </div>
    )
  }

  return (
    <>
      {renderButton()}
      {dialogInfo && (
        <KubectlCommandDialog info={dialogInfo} onClose={() => setDialogInfo(null)} />
      )}
    </>
  )
}

// Simplified inline button for use in port lists (shows just the port)
interface PortForwardInlineButtonProps {
  namespace: string
  podName?: string
  serviceName?: string
  port: number
  protocol?: string
  disabled?: boolean
}

export function PortForwardInlineButton({
  namespace,
  podName,
  serviceName,
  port,
  protocol = 'TCP',
  disabled = false,
}: PortForwardInlineButtonProps) {
  const { data: clusterInfo } = useClusterInfo()
  const startPortForward = useStartPortForward()
  const [dialogInfo, setDialogInfo] = useState<KubectlDialogInfo | null>(null)

  const inCluster = clusterInfo?.inCluster ?? false
  const isPending = !inCluster && startPortForward.isPending

  const handleClick = (e: React.MouseEvent) => {
    e.stopPropagation()
    if (inCluster) {
      const resourceType = serviceName ? 'service' : 'pod'
      const resourceName = serviceName || podName || ''
      setDialogInfo({ type: resourceType, namespace, name: resourceName, port })
    } else {
      startPortForward.mutate({
        namespace,
        podName,
        serviceName,
        podPort: port,
      })
    }
  }

  return (
    <>
      <button
        onClick={handleClick}
        disabled={disabled || isPending}
        className="inline-flex items-center gap-1 px-1.5 py-0.5 bg-slate-600/50 hover:bg-blue-600/50 rounded text-xs transition-colors disabled:opacity-50 disabled:hover:bg-slate-600/50"
        title={`Port forward ${port}`}
      >
        {port}/{protocol}
        {isPending ? (
          <Loader2 className="w-3 h-3 animate-spin" />
        ) : (
          <Plug className="w-3 h-3" />
        )}
      </button>
      {dialogInfo && (
        <KubectlCommandDialog info={dialogInfo} onClose={() => setDialogInfo(null)} />
      )}
    </>
  )
}
