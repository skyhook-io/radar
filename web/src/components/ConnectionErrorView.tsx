import { ServerOff, RefreshCw, Loader2, Copy, Check, TerminalSquare, ChevronRight } from 'lucide-react'
import { useEffect, useState } from 'react'
import type { ConnectionState } from '../context/ConnectionContext'
import { ContextSwitcher } from './ContextSwitcher'
import { parseContextName } from '../utils/context-name'
import { useOpenLocalTerminal, ClusterName } from '@skyhook-io/k8s-ui'
import { useAuthMe } from '../api/client'
import { Tooltip } from './ui/Tooltip'

interface ConnectionErrorViewProps {
  connection: ConnectionState
  onRetry: () => void
  isRetrying: boolean
}

interface AuthHints {
  title: string
  hints: string[]
  /** Primary auth command — usually sufficient on its own */
  authCommand?: { label: string; command: string }
  /** Secondary command shown as fallback if primary doesn't resolve the issue */
  fallbackCommand?: { label: string; command: string }
}

function getAuthHints(context: string): AuthHints {
  const parsed = parseContextName(context)

  switch (parsed.provider) {
    case 'GKE': {
      const result: AuthHints = {
        title: 'GKE Authentication Failed',
        hints: ['Radar could not get Google Cloud credentials for this context.'],
        authCommand: { label: 'Refresh Google Cloud credentials:', command: 'gcloud auth login' },
      }
      if (parsed.region && parsed.account) {
        const isZone = /^[a-z]+-[a-z]+\d+-[a-z]$/.test(parsed.region)
        const flag = isZone ? '--zone' : '--region'
        result.fallbackCommand = {
          label: 'If that doesn\'t work, refresh cluster credentials:',
          command: `gcloud container clusters get-credentials ${parsed.clusterName} ${flag} ${parsed.region} --project ${parsed.account}`,
        }
      }
      return result
    }
    case 'EKS': {
      const result: AuthHints = {
        title: 'EKS Authentication Failed',
        hints: [
          'Radar could not get AWS credentials for this context.',
          'For AWS SSO contexts, the SSO session may need login.',
        ],
        authCommand: { label: 'If this context uses AWS SSO, refresh credentials:', command: 'aws sso login' },
      }
      if (parsed.region) {
        result.fallbackCommand = {
          label: 'If that doesn\'t work, refresh cluster credentials:',
          command: `aws eks update-kubeconfig --name ${parsed.clusterName} --region ${parsed.region}`,
        }
      }
      return result
    }
    case 'AKS':
      return {
        title: 'AKS Authentication Failed',
        hints: ['Radar could not get Azure credentials for this context.'],
        authCommand: { label: 'Refresh Azure credentials:', command: 'az login' },
        fallbackCommand: { label: 'If that doesn\'t work, refresh cluster credentials:', command: 'az aks get-credentials --name <cluster> --resource-group <rg>' },
      }
    default:
      return {
        title: 'Authentication Failed',
        hints: [
          'Radar could not get Kubernetes credentials for this context',
          'Re-authenticate with your cloud provider and try again',
        ],
      }
  }
}

function getTimeoutHints(context: string): AuthHints | null {
  const parsed = parseContextName(context)
  const baseHints = [
    'The Kubernetes API did not respond before the deadline.',
    'Check VPN, firewall rules, and whether the cluster endpoint is reachable.',
  ]

  switch (parsed.provider) {
    case 'GKE': {
      const result: AuthHints = {
        title: 'Connection Timed Out',
        hints: [...baseHints, 'If the endpoint is reachable, Google Cloud credentials may need refresh.'],
        authCommand: { label: 'If network access looks healthy, refresh Google Cloud credentials:', command: 'gcloud auth login' },
      }
      if (parsed.region && parsed.account) {
        const isZone = /^[a-z]+-[a-z]+\d+-[a-z]$/.test(parsed.region)
        const flag = isZone ? '--zone' : '--region'
        result.fallbackCommand = {
          label: 'If that does not work, refresh cluster credentials:',
          command: `gcloud container clusters get-credentials ${parsed.clusterName} ${flag} ${parsed.region} --project ${parsed.account}`,
        }
      }
      return result
    }
    case 'EKS': {
      const result: AuthHints = {
        title: 'Connection Timed Out',
        hints: [...baseHints, 'If the endpoint is reachable, AWS credentials or SSO may need refresh.'],
        authCommand: { label: 'If this context uses AWS SSO and network access looks healthy, refresh credentials:', command: 'aws sso login' },
      }
      if (parsed.region) {
        result.fallbackCommand = {
          label: 'If that does not work, refresh cluster credentials:',
          command: `aws eks update-kubeconfig --name ${parsed.clusterName} --region ${parsed.region}`,
        }
      }
      return result
    }
    case 'AKS':
      return {
        title: 'Connection Timed Out',
        hints: [...baseHints, 'If the endpoint is reachable, Azure credentials may need refresh.'],
        authCommand: { label: 'If network access looks healthy, refresh Azure credentials:', command: 'az login' },
        fallbackCommand: { label: 'If that does not work, refresh cluster credentials:', command: 'az aks get-credentials --name <cluster> --resource-group <rg>' },
      }
    default:
      return null
  }
}

const errorHints: Record<string, { title: string; hints: string[] }> = {
  config: {
    title: 'No Kubeconfig Found',
    hints: [
      'Radar could not find a kubeconfig file at ~/.kube/config',
      'If your kubeconfig is at a custom path, set the KUBECONFIG environment variable in your shell profile (~/.zshrc or ~/.bashrc)',
      'You can also pass --kubeconfig <path> when launching from the terminal',
    ],
  },
  rbac: {
    title: 'Insufficient Permissions',
    hints: [
      'Your user account can connect but lacks required RBAC permissions',
      'Ask your cluster admin for a ClusterRole with list/watch access',
      'For read-only access, the built-in "view" ClusterRole is usually sufficient',
      'You can also try: kubectl auth can-i --list',
    ],
  },
  network: {
    title: 'Network Unreachable',
    hints: [
      'The cluster may be unreachable from your network',
      'Check if VPN connection is required',
      'Verify firewall rules allow access',
      'Confirm the cluster is running',
    ],
  },
  tls: {
    title: 'Certificate Error',
    hints: [
      'Radar reached the Kubernetes API, but could not verify its TLS certificate',
      'Check the kubeconfig cluster server hostname and certificate-authority settings',
      'If this cluster intentionally uses a private CA, refresh the kubeconfig for this context',
    ],
  },
  timeout: {
    title: 'Connection Timed Out',
    hints: [
      'The cluster is taking too long to respond',
      'The cluster may be under heavy load',
      'Network latency may be too high',
      'Try again or check cluster health',
    ],
  },
  unknown: {
    title: 'Connection Failed',
    hints: [
      'Check your kubeconfig is valid',
      'Verify the cluster endpoint is correct',
      'Try switching to a different context',
    ],
  },
}

function CopyableCommand({ command, onRunInTerminal }: { command: string; onRunInTerminal?: (command: string) => void }) {
  const [copied, setCopied] = useState(false)
  const commandParts = command.split(/(\s+)/)

  const handleCopy = () => {
    navigator.clipboard.writeText(command).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    }).catch(() => {
      // Clipboard API may be unavailable (e.g., non-HTTPS context)
    })
  }

  return (
    <div className="mt-2 flex items-center gap-2 bg-theme-elevated border border-theme-border rounded-md px-3 py-2 group">
      <code className="text-xs font-mono text-theme-text-primary flex-1 min-w-0 select-all whitespace-pre-wrap break-normal">
        {commandParts.map((part, index) => (
          /\s+/.test(part) ? part : <span key={index} className="inline-block whitespace-nowrap">{part}</span>
        ))}
      </code>
      {onRunInTerminal && (
        <Tooltip content="Run in terminal" wrapperClassName="shrink-0">
          <button
            type="button"
            onClick={() => onRunInTerminal(command)}
            aria-label="Run command in terminal"
            className="shrink-0 text-theme-text-tertiary hover:text-theme-text-secondary transition-colors"
          >
            <TerminalSquare className="w-3.5 h-3.5" />
          </button>
        </Tooltip>
      )}
      <Tooltip content="Copy to clipboard" wrapperClassName="shrink-0">
        <button
          type="button"
          onClick={handleCopy}
          aria-label="Copy command to clipboard"
          className="shrink-0 text-theme-text-tertiary hover:text-theme-text-secondary transition-colors"
        >
          {copied ? (
            <Check className="w-3.5 h-3.5 text-green-400" />
          ) : (
            <Copy className="w-3.5 h-3.5" />
          )}
        </button>
      </Tooltip>
    </div>
  )
}

export function ConnectionErrorView({ connection, onRetry, isRetrying }: ConnectionErrorViewProps) {
  // For auth errors, generate context-aware hints with a specific re-auth command
  const isAuth = connection.errorType === 'auth'
  const isTimeout = connection.errorType === 'timeout'
  const commandInfo = isAuth
    ? getAuthHints(connection.context || '')
    : isTimeout
      ? getTimeoutHints(connection.context || '')
      : null
  const errorInfo = commandInfo || errorHints[connection.errorType || 'unknown'] || errorHints.unknown
  const openLocalTerminal = useOpenLocalTerminal()
  const { data: authMe } = useAuthMe()
  const rawErrorDefaultOpen = !connection.errorType || connection.errorType === 'unknown'
  const [showRawError, setShowRawError] = useState(rawErrorDefaultOpen)

  useEffect(() => {
    setShowRawError(rawErrorDefaultOpen)
  }, [connection.error, rawErrorDefaultOpen])

  // Auto-retry after successful auth. The terminal shell runs on the server
  // host, so the auth command itself fixes the server's credentials in every
  // mode — but the chained retry curl carries no session cookie, so it 401s
  // once /api/connection is auth-gated. Only chain it when auth is *known*
  // disabled (authMe still loading → don't chain a doomed call).
  const retryCmd = `curl -s -X POST http://${window.location.host}/api/connection/retry > /dev/null`

  const handleAuthInTerminal = () => {
    if (!commandInfo?.authCommand) return
    const cmd = authMe?.authEnabled === false
      ? `${commandInfo.authCommand.command} && ${retryCmd}`
      : commandInfo.authCommand.command
    openLocalTerminal({
      initialCommand: cmd,
      title: 'Auth',
    })
  }

  const handleRunInTerminal = (command: string) => {
    openLocalTerminal({ initialCommand: command, title: 'Auth' })
  }

  return (
    <div className="flex-1 flex items-start justify-center pt-12 px-8">
      <div className="max-w-xl w-full">
        <div className="flex flex-col items-center text-center">
          <div className="w-14 h-14 rounded-full bg-red-500/10 flex items-center justify-center mb-5">
            <ServerOff className="w-8 h-8 text-red-400" />
          </div>

          <h2 className="text-xl font-semibold text-theme-text-primary mb-2">
            {connection.errorType === 'config' ? 'No Cluster Configuration' : 'Cannot Connect to Cluster'}
          </h2>

          <div className="mb-6 space-y-1">
            <p className="text-sm text-theme-text-secondary inline-flex items-center gap-1.5">
              Context: {connection.context ? (
                <ClusterName name={connection.context} />
              ) : (
                <span className="inline-code">(none)</span>
              )}
            </p>

            {connection.clusterName && (
              <p className="text-sm text-theme-text-secondary">
                Cluster: <span className="inline-code">{connection.clusterName}</span>
              </p>
            )}
          </div>

          <div className="w-full bg-theme-surface border border-theme-border rounded-lg p-4 mb-5 text-left">
            <h3 className="text-sm font-medium text-theme-text-primary mb-2">
              {errorInfo.title}
            </h3>
            <ul className="text-sm text-theme-text-secondary space-y-1">
              {errorInfo.hints.map((hint, i) => (
                <li key={i} className="flex items-start gap-2">
                  <span className="text-theme-text-tertiary mt-0.5">-</span>
                  <span>{hint}</span>
                </li>
              ))}
            </ul>
            {commandInfo?.authCommand && (
              <div className="mt-3">
                <p className="text-xs text-theme-text-tertiary">{commandInfo.authCommand.label}</p>
                <CopyableCommand command={commandInfo.authCommand.command} onRunInTerminal={handleRunInTerminal} />
                {isAuth && (
                  <button
                    onClick={handleAuthInTerminal}
                    className="mt-3 w-full inline-flex items-center justify-center gap-2 px-3 py-2 text-xs font-medium btn-brand rounded-md"
                  >
                    <TerminalSquare className="w-3.5 h-3.5" />
                    Authenticate in terminal
                  </button>
                )}
              </div>
            )}
            {commandInfo?.fallbackCommand && (
              <div className="mt-4 pt-3 border-t border-theme-border/50">
                <p className="text-xs text-theme-text-tertiary">{commandInfo.fallbackCommand.label}</p>
                <CopyableCommand command={commandInfo.fallbackCommand.command} onRunInTerminal={handleRunInTerminal} />
              </div>
            )}
            {connection.error && (
              <div className="mt-4 pt-3 border-t border-theme-border/50">
                <button
                  type="button"
                  aria-expanded={showRawError}
                  aria-controls="connection-raw-error"
                  onClick={() => setShowRawError((open) => !open)}
                  className="flex items-center gap-1 text-xs font-medium text-theme-text-tertiary hover:text-theme-text-secondary transition-colors"
                >
                  <ChevronRight className={`h-3.5 w-3.5 transition-transform duration-200 ${showRawError ? 'rotate-90' : ''}`} />
                  Raw error
                </button>
                <div className={`issue-details-motion ${showRawError ? 'issue-details-motion-open' : ''}`}>
                  <div className="overflow-hidden">
                    <div id="connection-raw-error" className="mt-2 bg-theme-elevated border border-theme-border rounded-md p-3 overflow-auto max-h-32">
                      <code className="text-xs text-theme-text-tertiary font-mono whitespace-pre-wrap break-words">
                        {connection.error}
                      </code>
                    </div>
                  </div>
                </div>
              </div>
            )}
          </div>

          <div className="flex items-center gap-5">
            <button
              onClick={onRetry}
              disabled={isRetrying}
              className="inline-flex items-center gap-2 px-4 py-2 btn-brand rounded-lg"
            >
              {isRetrying ? (
                <>
                  <Loader2 className="w-4 h-4 animate-spin" />
                  Connecting...
                </>
              ) : (
                <>
                  <RefreshCw className="w-4 h-4" />
                  Retry Connection
                </>
              )}
            </button>

            {connection.errorType !== 'config' && <ContextSwitcher triggerName="Switch context" />}
          </div>

          {isAuth && (
            <p className="mt-4 text-xs text-theme-text-tertiary">
              Radar will keep retrying after credentials are refreshed.
            </p>
          )}
        </div>
      </div>
    </div>
  )
}
