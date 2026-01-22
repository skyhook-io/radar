import { useState, useRef, useEffect } from 'react'
import { Plug, ChevronDown, Loader2 } from 'lucide-react'
import { clsx } from 'clsx'
import { useAvailablePorts, AvailablePort } from '../../api/client'
import { useStartPortForward } from './PortForwardManager'

interface PortForwardButtonProps {
  type: 'pod' | 'service'
  namespace: string
  name: string
  // For service port forwarding
  serviceName?: string
  className?: string
}

export function PortForwardButton({
  type,
  namespace,
  name,
  serviceName,
  className,
}: PortForwardButtonProps) {
  const [isOpen, setIsOpen] = useState(false)
  const dropdownRef = useRef<HTMLDivElement>(null)

  const { data, isLoading } = useAvailablePorts(type, namespace, name)
  const startPortForward = useStartPortForward()

  const ports = data?.ports || []

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
    startPortForward.mutate({
      namespace,
      podName: type === 'pod' ? name : undefined,
      serviceName: type === 'service' ? (serviceName || name) : undefined,
      podPort: port.port,
    })
  }

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

  // If only one port, forward directly on click
  if (ports.length === 1) {
    return (
      <button
        onClick={() => handlePortSelect(ports[0])}
        disabled={startPortForward.isPending}
        className={clsx(
          'flex items-center gap-2 px-3 py-2 bg-slate-700 text-white text-sm rounded-lg hover:bg-slate-600 transition-colors disabled:opacity-50',
          className
        )}
        title={`Port forward to ${ports[0].port}`}
      >
        {startPortForward.isPending ? (
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
        disabled={isLoading || startPortForward.isPending}
        className={clsx(
          'flex items-center gap-2 px-3 py-2 bg-slate-700 text-white text-sm rounded-lg hover:bg-slate-600 transition-colors disabled:opacity-50',
          className
        )}
      >
        {isLoading || startPortForward.isPending ? (
          <Loader2 className="w-4 h-4 animate-spin" />
        ) : (
          <Plug className="w-4 h-4" />
        )}
        Port Forward
        <ChevronDown className={clsx('w-3 h-3 transition-transform', isOpen && 'rotate-180')} />
      </button>

      {isOpen && (
        <div className="absolute top-full left-0 mt-1 w-56 bg-slate-800 border border-slate-700 rounded-lg shadow-xl z-50 py-1">
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
  const startPortForward = useStartPortForward()

  const handleClick = (e: React.MouseEvent) => {
    e.stopPropagation()
    startPortForward.mutate({
      namespace,
      podName,
      serviceName,
      podPort: port,
    })
  }

  return (
    <button
      onClick={handleClick}
      disabled={disabled || startPortForward.isPending}
      className="inline-flex items-center gap-1 px-1.5 py-0.5 bg-slate-600/50 hover:bg-blue-600/50 rounded text-xs transition-colors disabled:opacity-50 disabled:hover:bg-slate-600/50"
      title={`Port forward ${port}`}
    >
      {port}/{protocol}
      {startPortForward.isPending ? (
        <Loader2 className="w-3 h-3 animate-spin" />
      ) : (
        <Plug className="w-3 h-3" />
      )}
    </button>
  )
}
