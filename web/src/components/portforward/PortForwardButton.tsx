import { useState, useRef, useEffect } from 'react'
import { createPortal } from 'react-dom'
import { Plug, ChevronDown, Loader2, Globe, Monitor, Copy, Check, X, Terminal } from 'lucide-react'
import { clsx } from 'clsx'
import { useAvailablePorts, AvailablePort } from '../../api/client'
import { useStartPortForward } from './PortForwardManager'
import { useIsLocalDeployment } from '../../contexts/CapabilitiesContext'
import { validatePort } from '@skyhook-io/k8s-ui/utils/validators'
import { Tooltip } from '../ui/Tooltip'
import { Input } from '@skyhook-io/k8s-ui'

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

// kubectl port-forward (and the live forward, which uses the same transport) is
// TCP-only — UDP/SCTP can't be forwarded (kubernetes/kubernetes#47862). Treat an
// unset protocol as TCP.
export function isPortForwardable(protocol?: string): boolean {
  return (protocol || 'TCP').toUpperCase() === 'TCP'
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
  // Track raw input separately from the validated port so the user
  // always sees the characters they typed; the validated port (used to
  // build the command) only updates when the input parses cleanly.
  const [portInput, setPortInput] = useState(String(info.port))
  const portValidation = validatePort(portInput)
  const localPort = portValidation.valid ? portValidation.value : info.port
  const portError = portValidation.valid ? null : portValidation.error
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
      if (e.key !== 'Escape') return
      // Capture + stopPropagation so Escape closes only this dialog, not the
      // drawer behind it (whose Escape shortcut listens in the bubble phase).
      e.stopPropagation()
      onClose()
    }
    document.addEventListener('keydown', handleKeyDown, true)
    return () => document.removeEventListener('keydown', handleKeyDown, true)
  }, [onClose])

  useEffect(() => {
    dialogRef.current?.focus()
  }, [])

  // Portal to <body>: the drawer is a transformed ancestor that would otherwise
  // trap this position:fixed dialog inside the drawer instead of centering it.
  return createPortal(
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      <div className="absolute inset-0 bg-black/60 backdrop-blur-sm" onClick={onClose} />
      <div
        ref={dialogRef}
        tabIndex={-1}
        className="relative dialog max-w-lg w-full mx-4 outline-none"
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
            Forward this port to your own machine — run it from your terminal:
          </p>
          <div className="flex flex-col gap-1">
            <div className="flex items-center gap-2 text-sm text-theme-text-secondary">
              <label htmlFor="local-port">Local port:</label>
              <Input
                id="local-port"
                inputMode="numeric"
                value={portInput}
                onChange={(e) => setPortInput(e.target.value)}
                aria-invalid={portError ? true : undefined}
                aria-describedby="local-port-help"
                className={clsx(
                  'w-24 bg-theme-base border rounded px-2 py-1 text-sm text-theme-text-primary font-mono text-center',
                  portError
                    ? 'border-red-500/60 focus:outline-none focus:ring-2 focus:ring-red-500'
                    : 'border-theme-border',
                )}
              />
              {portError && (
                <span className="text-xs text-red-400">
                  using {info.port}
                </span>
              )}
            </div>
            {portError && (
              <p id="local-port-help" className="text-xs text-red-400">
                {portError.charAt(0).toUpperCase() + portError.slice(1)}.
              </p>
            )}
          </div>
          <div className="flex items-center gap-2">
            <code ref={commandRef} className="flex-1 text-sm bg-theme-base rounded px-3 py-2 text-blue-400 font-mono select-all">
              {command}
            </code>
            <button
              onClick={handleCopy}
              className="shrink-0 px-3 py-2 btn-brand text-sm rounded-lg flex items-center gap-1.5"
            >
              {copied ? <Check className="w-4 h-4" /> : <Copy className="w-4 h-4" />}
              {copied ? 'Copied' : copyFallback ? 'Press Ctrl+C' : 'Copy'}
            </button>
          </div>
          <p className="text-xs text-theme-text-tertiary">
            You&apos;ll need <code className="inline-code">kubectl</code> and access to this cluster.
          </p>
        </div>
      </div>
    </div>,
    document.body,
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

  const isLocal = useIsLocalDeployment()
  const { data, isLoading } = useAvailablePorts(type, namespace, name)
  const startPortForward = useStartPortForward()

  const ports = data?.ports || []
  // Decide copy-command vs live forward from the SAME deployment signal that
  // gates whether the button shows at all — so the two can't disagree (and we
  // don't race a separate /cluster-info fetch that defaults to "not in-cluster").
  // Cloud runs in-cluster too, so anything not-local uses the copy command.
  const inCluster = !isLocal
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
    // kubectl port-forward is TCP-only — never offer UDP/SCTP ports as targets.
    const forwardable = ports.filter((p) => isPortForwardable(p.protocol))

    // No forwardable ports: disabled button. Distinguish "no ports at all" from
    // "ports exist but are all UDP" so the operator isn't left guessing.
    if (!isLoading && forwardable.length === 0) {
      const udpOnly = ports.length > 0
      return (
        <Tooltip content={udpOnly ? "kubectl port-forward doesn't support UDP" : 'No ports available'}>
        <button
          disabled
          className={clsx(
            'flex items-center gap-2 px-3 py-2 bg-theme-elevated text-theme-text-primary text-sm rounded-lg opacity-50 cursor-not-allowed disabled:pointer-events-none',
            className
          )}
        >
          <Plug className="w-4 h-4" />
          {udpOnly ? 'No TCP Ports' : 'No Ports'}
        </button>
        </Tooltip>
      )
    }

    // If only one forwardable port, forward directly on click (most common case)
    if (forwardable.length === 1) {
      return (
        <Tooltip content={`Port forward to ${forwardable[0].port}`}>
        <button
          onClick={() => handlePortSelect(forwardable[0])}
          disabled={isPending}
          className={clsx(
            'flex items-center gap-2 px-3 py-2 bg-theme-elevated text-theme-text-primary text-sm rounded-lg hover:bg-theme-hover transition-colors disabled:opacity-50 disabled:pointer-events-none',
            className
          )}
        >
          {isPending ? (
            <Loader2 className="w-4 h-4 animate-spin" />
          ) : (
            <Plug className="w-4 h-4" />
          )}
          Forward :{forwardable[0].port}
        </button>
        </Tooltip>
      )
    }

    // Multiple ports - show dropdown
    return (
      <div className="relative" ref={dropdownRef}>
        <button
          onClick={() => setIsOpen(!isOpen)}
          disabled={isLoading || isPending}
          className={clsx(
            'flex items-center gap-2 px-3 py-2 bg-theme-elevated text-theme-text-primary text-sm rounded-lg hover:bg-theme-hover transition-colors disabled:opacity-50',
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
          <div className="absolute top-full left-0 mt-1 w-64 bg-theme-surface border border-theme-border rounded-lg shadow-xl z-50 py-1">
            {/* Listen address toggle - only for local mode */}
            {!inCluster && (
              <div className="px-3 py-2 border-b border-theme-border">
                <div className="text-xs text-theme-text-disabled mb-2">Listen on</div>
                <div className="flex gap-1">
                  <Tooltip content="Only accessible from this machine" wrapperClassName="flex-1">
                  <button
                    onClick={(e) => { e.stopPropagation(); setListenAddress('127.0.0.1') }}
                    className={clsx(
                      'flex-1 flex items-center justify-center gap-1.5 px-2 py-1.5 text-xs rounded transition-colors',
                      listenAddress === '127.0.0.1'
                        ? 'btn-brand-toggle'
                        : 'bg-theme-elevated text-theme-text-tertiary hover:text-theme-text-primary'
                    )}
                  >
                    <Monitor className="w-3 h-3" />
                    localhost
                  </button>
                  </Tooltip>
                  <Tooltip content="Accessible from other machines on the network" wrapperClassName="flex-1">
                  <button
                    onClick={(e) => { e.stopPropagation(); setListenAddress('0.0.0.0') }}
                    className={clsx(
                      'flex-1 flex items-center justify-center gap-1.5 px-2 py-1.5 text-xs rounded transition-colors',
                      listenAddress === '0.0.0.0'
                        ? 'bg-amber-600 text-white'
                        : 'bg-theme-elevated text-theme-text-tertiary hover:text-theme-text-primary'
                    )}
                  >
                    <Globe className="w-3 h-3" />
                    all interfaces
                  </button>
                  </Tooltip>
                </div>
              </div>
            )}
            <div className="px-2 py-1.5 text-xs text-theme-text-disabled border-b border-theme-border">
              Select port to forward
            </div>
            {forwardable.map((port, i) => (
              <button
                key={i}
                onClick={() => handlePortSelect(port)}
                className="w-full px-3 py-2 text-left text-sm text-theme-text-primary hover:bg-theme-elevated flex items-center justify-between"
              >
                <span className="flex items-center gap-2 shrink-0">
                  <code className="inline-code">{port.port}</code>
                  <span className="text-theme-text-disabled">/{port.protocol || 'TCP'}</span>
                </span>
                {port.name && (
                  <span className="text-xs text-theme-text-disabled truncate max-w-[120px]">{port.name}</span>
                )}
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
  const isLocal = useIsLocalDeployment()
  const startPortForward = useStartPortForward()
  const [dialogInfo, setDialogInfo] = useState<KubectlDialogInfo | null>(null)

  // Decide copy-command vs live forward from the SAME deployment signal that
  // gates whether the button shows at all — so the two can't disagree (and we
  // don't race a separate /cluster-info fetch that defaults to "not in-cluster").
  // Cloud runs in-cluster too, so anything not-local uses the copy command.
  const inCluster = !isLocal
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

  // UDP/SCTP can't be port-forwarded — show a muted, non-interactive hint that
  // explains why rather than a button that would copy a command that can't work.
  if (!isPortForwardable(protocol)) {
    return (
      <Tooltip content="kubectl port-forward doesn't support UDP">
        <span className="inline-flex items-center gap-1 px-1.5 py-0.5 bg-theme-elevated rounded text-xs text-theme-text-tertiary opacity-60 cursor-default">
          {port}/{protocol}
          <Plug className="w-3 h-3" />
        </span>
      </Tooltip>
    )
  }

  return (
    <>
      <Tooltip content={inCluster ? 'Copy a kubectl port-forward command' : `Port forward ${port}`}>
      <button
        onClick={handleClick}
        disabled={disabled || isPending}
        className="inline-flex items-center gap-1 px-1.5 py-0.5 bg-theme-elevated hover:bg-accent-muted rounded text-xs transition-colors disabled:opacity-50 disabled:hover:bg-theme-elevated disabled:pointer-events-none"
      >
        {/* In-cluster this opens a copy-command dialog rather than forwarding now;
            the trailing "…" signals "opens a dialog" (it doesn't fire immediately). */}
        {port}/{protocol}{inCluster ? '…' : ''}
        {isPending ? (
          <Loader2 className="w-3 h-3 animate-spin" />
        ) : (
          <Plug className="w-3 h-3" />
        )}
      </button>
      </Tooltip>
      {dialogInfo && (
        <KubectlCommandDialog info={dialogInfo} onClose={() => setDialogInfo(null)} />
      )}
    </>
  )
}
