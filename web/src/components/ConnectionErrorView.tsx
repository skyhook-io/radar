import { XCircle, RefreshCw, Loader2, Copy, Check, TerminalSquare } from 'lucide-react'
import { useState } from 'react'
import type { ConnectionState } from '../context/ConnectionContext'
import { ContextSwitcher } from './ContextSwitcher'
import { parseContextName } from '../utils/context-name'
import { useOpenLocalTerminal, ClusterName } from '@skyhook-io/k8s-ui'
import { useAuthMe } from '../api/client'

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
        hints: ['Your Google Cloud credentials have expired.'],
        authCommand: { label: 'Re-authenticate with Google Cloud:', command: 'gcloud auth login' },
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
        hints: ['Your AWS credentials have expired.'],
        authCommand: { label: 'Re-authenticate with AWS:', command: 'aws sso login' },
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
        hints: ['Your Azure credentials have expired.'],
        authCommand: { label: 'Re-authenticate with Azure:', command: 'az login' },
        fallbackCommand: { label: 'If that doesn\'t work, refresh cluster credentials:', command: 'az aks get-credentials --name <cluster> --resource-group <rg>' },
      }
    default:
      return {
        title: 'Authentication Failed',
        hints: [
          'Your credentials may have expired',
          'Re-authenticate with your cloud provider and try again',
        ],
      }
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
      <code className="text-xs font-mono text-theme-text-primary flex-1 select-all break-all">
        {command}
      </code>
      {onRunInTerminal && (
        <button
          onClick={() => onRunInTerminal(command)}
          className="shrink-0 text-theme-text-tertiary hover:text-theme-text-secondary transition-colors"
          title="Run in terminal"
        >
          <TerminalSquare className="w-3.5 h-3.5" />
        </button>
      )}
      <button
        onClick={handleCopy}
        className="shrink-0 text-theme-text-tertiary hover:text-theme-text-secondary transition-colors"
        title="Copy to clipboard"
      >
        {copied ? (
          <Check className="w-3.5 h-3.5 text-green-400" />
        ) : (
          <Copy className="w-3.5 h-3.5" />
        )}
      </button>
    </div>
  )
}

export function ConnectionErrorView({ connection, onRetry, isRetrying }: ConnectionErrorViewProps) {
  // For auth errors, generate context-aware hints with a specific re-auth command
  const isAuth = connection.errorType === 'auth'
  const authInfo = isAuth ? getAuthHints(connection.context || '') : null
  const errorInfo = authInfo || errorHints[connection.errorType || 'unknown'] || errorHints.unknown
  const openLocalTerminal = useOpenLocalTerminal()
  const { data: authMe } = useAuthMe()

  // Auto-retry after successful auth. The terminal shell runs on the server
  // host, so the auth command itself fixes the server's credentials in every
  // mode — but the chained retry curl carries no session cookie, so it 401s
  // once /api/connection is auth-gated. Only chain it when auth is *known*
  // disabled (authMe still loading → don't chain a doomed call).
  const retryCmd = `curl -s -X POST http://${window.location.host}/api/connection/retry > /dev/null`

  const handleAuthInTerminal = () => {
    if (!authInfo?.authCommand) return
    const cmd = authMe?.authEnabled === false
      ? `${authInfo.authCommand.command} && ${retryCmd}`
      : authInfo.authCommand.command
    openLocalTerminal({
      initialCommand: cmd,
      title: 'Auth',
    })
  }

  const handleRunInTerminal = (command: string) => {
    openLocalTerminal({ initialCommand: command, title: 'Auth' })
  }

  return (
    <div className="flex-1 flex items-start justify-center pt-16 px-8">
      <div className="max-w-lg w-full">
        <div className="flex flex-col items-center text-center">
          <div className="w-16 h-16 rounded-full bg-red-500/10 flex items-center justify-center mb-6">
            <XCircle className="w-10 h-10 text-red-400" />
          </div>

          <h2 className="text-xl font-semibold text-theme-text-primary mb-2">
            {connection.errorType === 'config' ? 'No Cluster Configuration' : 'Cannot Connect to Cluster'}
          </h2>

          <p className="text-sm text-theme-text-secondary mb-1 inline-flex items-center gap-1.5">
            Context: {connection.context ? (
              <ClusterName name={connection.context} />
            ) : (
              <span className="inline-code">(none)</span>
            )}
          </p>

          {connection.clusterName && (
            <p className="text-sm text-theme-text-secondary mb-4">
              Cluster: <span className="inline-code">{connection.clusterName}</span>
            </p>
          )}

          <div className="w-full bg-theme-surface border border-theme-border rounded-lg p-4 mb-6 text-left">
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
            {authInfo?.authCommand && (
              <div className="mt-3">
                <p className="text-xs text-theme-text-tertiary">{authInfo.authCommand.label}</p>
                <CopyableCommand command={authInfo.authCommand.command} onRunInTerminal={handleRunInTerminal} />
                <button
                  onClick={handleAuthInTerminal}
                  className="mt-3 w-full inline-flex items-center justify-center gap-2 px-3 py-2 text-xs font-medium btn-brand rounded-md"
                >
                  <TerminalSquare className="w-3.5 h-3.5" />
                  Authenticate in terminal
                </button>
              </div>
            )}
            {authInfo?.fallbackCommand && (
              <div className="mt-4 pt-3 border-t border-theme-border/50">
                <p className="text-xs text-theme-text-tertiary">{authInfo.fallbackCommand.label}</p>
                <CopyableCommand command={authInfo.fallbackCommand.command} onRunInTerminal={handleRunInTerminal} />
              </div>
            )}
          </div>

          {connection.error && (
            <div className="w-full bg-theme-elevated border border-theme-border rounded-lg p-3 mb-6 overflow-auto max-h-32">
              <code className="text-xs text-red-400 font-mono whitespace-pre-wrap break-all">
                {connection.error}
              </code>
            </div>
          )}

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

            {connection.errorType !== 'config' && <ContextSwitcher />}
          </div>
        </div>
      </div>
    </div>
  )
}
