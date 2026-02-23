import { useRef, useEffect, useState } from 'react'
import { X, Copy, Check, Radio, Terminal, MessageSquare, Code2, ChevronRight } from 'lucide-react'

interface MCPSetupDialogProps {
  open: boolean
  onClose: () => void
  mcpUrl: string
}

function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false)

  const handleCopy = () => {
    navigator.clipboard.writeText(text)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }

  return (
    <button
      onClick={handleCopy}
      className="absolute top-2 right-2 p-1.5 rounded-md bg-theme-elevated/50 hover:bg-theme-elevated text-theme-text-tertiary hover:text-theme-text-secondary transition-colors cursor-pointer"
      title="Copy to clipboard"
    >
      {copied ? <Check className="w-3.5 h-3.5 text-green-500" /> : <Copy className="w-3.5 h-3.5" />}
    </button>
  )
}

function CodeBlock({ children }: { children: string }) {
  return (
    <div className="relative group">
      <pre className="bg-theme-base rounded-md px-3 py-2.5 text-xs font-mono text-theme-text-secondary overflow-x-auto whitespace-pre-wrap break-all">
        {children}
      </pre>
      <CopyButton text={children} />
    </div>
  )
}

export function MCPSetupDialog({ open, onClose, mcpUrl }: MCPSetupDialogProps) {
  const dialogRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [open, onClose])

  useEffect(() => {
    if (open && dialogRef.current) {
      dialogRef.current.focus()
    }
  }, [open])

  if (!open) return null

  const claudeDesktopConfig = JSON.stringify({
    mcpServers: {
      radar: {
        type: "http",
        url: mcpUrl,
      }
    }
  }, null, 2)

  const cursorConfig = JSON.stringify({
    mcpServers: {
      radar: {
        url: mcpUrl,
      }
    }
  }, null, 2)

  const windsurfConfig = JSON.stringify({
    mcpServers: {
      radar: {
        serverUrl: mcpUrl,
      }
    }
  }, null, 2)

  const vsCodeConfig = JSON.stringify({
    servers: {
      radar: {
        type: "http",
        url: mcpUrl,
      }
    }
  }, null, 2)

  const geminiConfig = JSON.stringify({
    mcpServers: {
      radar: {
        httpUrl: mcpUrl,
      }
    }
  }, null, 2)

  const codexConfig = `[mcp_servers.radar]\nurl = "${mcpUrl}"`

  const clineConfig = JSON.stringify({
    mcpServers: {
      radar: {
        url: mcpUrl,
      }
    }
  }, null, 2)

  const jetbrainsConfig = JSON.stringify({
    mcpServers: {
      radar: {
        url: mcpUrl,
      }
    }
  }, null, 2)

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      <div className="absolute inset-0 bg-black/60 backdrop-blur-sm" onClick={onClose} />
      <div
        ref={dialogRef}
        tabIndex={-1}
        className="relative bg-theme-surface border border-theme-border rounded-lg shadow-2xl max-w-2xl w-full mx-4 outline-none max-h-[85vh] flex flex-col"
      >
        {/* Header */}
        <div className="flex items-center justify-between px-6 py-4 border-b border-theme-border shrink-0">
          <div className="flex items-center gap-3">
            <Radio className="w-5 h-5 text-purple-400" />
            <h3 className="text-lg font-semibold text-theme-text-primary">MCP Server</h3>
          </div>
          <button onClick={onClose} className="p-1.5 hover:bg-theme-elevated rounded-md transition-colors cursor-pointer">
            <X className="w-5 h-5 text-theme-text-tertiary" />
          </button>
        </div>

        {/* Scrollable content */}
        <div className="overflow-y-auto flex-1 min-h-0 px-6 py-5 space-y-6">
          {/* Explanation */}
          <div className="space-y-3">
            <h4 className="text-sm font-semibold text-theme-text-primary">AI meets your cluster</h4>
            <p className="text-sm text-theme-text-secondary leading-relaxed">
              Radar exposes a{' '}
              <a href="https://modelcontextprotocol.io" target="_blank" rel="noopener noreferrer" className="text-purple-400 hover:text-purple-300 underline underline-offset-2">
                Model Context Protocol
              </a>{' '}
              (MCP) server that lets AI assistants query your cluster through Radar.
              Unlike raw kubectl access, Radar gives your AI pre-processed, enriched data —
              topology graphs, health assessments, deduplicated events, filtered logs — so it
              can understand your cluster state quickly without burning through context on
              verbose YAML output.
            </p>
            <p className="text-sm text-theme-text-secondary leading-relaxed">
              Read tools are strictly read-only. Write tools (restart, scale, sync) are
              non-destructive and annotated so your AI client can distinguish them.
            </p>
          </div>

          {/* Endpoint */}
          <div className="space-y-2">
            <h4 className="text-sm font-semibold text-theme-text-primary">Endpoint</h4>
            <div className="relative">
              <div className="flex items-center gap-3 bg-theme-base rounded-md px-3 py-2.5">
                <span className="text-xs font-medium text-purple-400 bg-purple-500/10 px-2 py-0.5 rounded">HTTP</span>
                <code className="text-sm font-mono text-theme-text-primary">{mcpUrl}</code>
              </div>
              <CopyButton text={mcpUrl} />
            </div>
          </div>

          {/* Setup instructions */}
          <div className="space-y-2">
            <h4 className="text-sm font-semibold text-theme-text-primary">Connect your AI tool</h4>

            {[
              { icon: Terminal, name: 'Claude Code', path: '', config: `claude mcp add radar --transport http ${mcpUrl}` },
              { icon: MessageSquare, name: 'Claude Desktop', path: '~/Library/Application Support/Claude/claude_desktop_config.json', config: claudeDesktopConfig },
              { icon: Code2, name: 'Cursor', path: '~/.cursor/mcp.json', config: cursorConfig },
              { icon: Code2, name: 'Windsurf', path: '~/.codeium/windsurf/mcp_config.json', config: windsurfConfig },
              { icon: Code2, name: 'VS Code Copilot', path: '.vscode/mcp.json', config: vsCodeConfig },
              { icon: Code2, name: 'Cline', path: 'Cline MCP settings (via UI)', config: clineConfig },
              { icon: Code2, name: 'JetBrains AI', path: 'Settings → Tools → AI Assistant → MCP', config: jetbrainsConfig },
              { icon: Terminal, name: 'OpenAI Codex', path: '~/.codex/config.toml', config: codexConfig },
              { icon: Terminal, name: 'Gemini CLI', path: '~/.gemini/settings.json', config: geminiConfig },
            ].map((agent) => (
              <details key={agent.name} className="group rounded-md border border-theme-border/50 bg-theme-base/30">
                <summary className="flex items-center gap-2 px-3 py-2 cursor-pointer select-none list-none hover:bg-theme-hover/50 rounded-md transition-colors [&::-webkit-details-marker]:hidden">
                  <ChevronRight className="w-3.5 h-3.5 text-theme-text-tertiary transition-transform group-open:rotate-90" />
                  <agent.icon className="w-4 h-4 text-theme-text-tertiary" />
                  <span className="text-sm font-medium text-theme-text-primary">{agent.name}</span>
                  {agent.path && <span className="text-[10px] text-theme-text-tertiary ml-auto">{agent.path}</span>}
                </summary>
                <div className="px-3 pb-3 pt-1">
                  <CodeBlock>{agent.config}</CodeBlock>
                </div>
              </details>
            ))}
          </div>

          {/* Available tools */}
          <div className="space-y-2">
            <h4 className="text-sm font-semibold text-theme-text-primary">Tools</h4>
            <div className="grid grid-cols-1 gap-1.5">
              {[
                { name: 'get_dashboard', desc: 'Get cluster health overview including resource counts, problems (failing pods, unhealthy deployments), recent warning events, and Helm release status. Start here to understand cluster state before drilling into specific resources.', params: [
                  { name: 'namespace', required: false, desc: 'filter to a specific namespace' },
                ]},
                { name: 'list_resources', desc: 'List Kubernetes resources of a given kind with minified summaries. Supports all built-in kinds (pods, deployments, services, etc.) and CRDs. Use to discover what\'s running before inspecting individual resources.', params: [
                  { name: 'kind', required: true, desc: 'resource kind, e.g. pods, deployments, services' },
                  { name: 'namespace', required: false, desc: 'filter to a specific namespace' },
                ]},
                { name: 'get_resource', desc: 'Get detailed information about a single Kubernetes resource. Returns minified spec, status, and metadata. Use after list_resources to drill into a specific resource.', params: [
                  { name: 'kind', required: true, desc: 'resource kind, e.g. pod, deployment, service' },
                  { name: 'namespace', required: true, desc: 'resource namespace' },
                  { name: 'name', required: true, desc: 'resource name' },
                ]},
                { name: 'get_topology', desc: 'Get the topology graph showing relationships between Kubernetes resources. Returns nodes and edges representing Deployments, Services, Ingresses, Pods, etc. Use \'traffic\' view for network flow or \'resources\' view for ownership hierarchy.', params: [
                  { name: 'namespace', required: false, desc: 'filter to a specific namespace' },
                  { name: 'view', required: false, desc: 'traffic or resources' },
                ]},
                { name: 'get_events', desc: 'Get recent Kubernetes warning events, deduplicated and sorted by recency. Useful for diagnosing issues — shows event reason, message, and occurrence count.', params: [
                  { name: 'namespace', required: false, desc: 'filter to a specific namespace' },
                  { name: 'limit', required: false, desc: 'max events to return (default 20)' },
                ]},
                { name: 'get_pod_logs', desc: 'Get filtered log lines from a pod, prioritizing errors and warnings. Returns diagnostically relevant lines (errors, panics, stack traces) or falls back to the last 20 lines if no error patterns match.', params: [
                  { name: 'namespace', required: true, desc: 'pod namespace' },
                  { name: 'name', required: true, desc: 'pod name' },
                  { name: 'container', required: false, desc: 'container name (defaults to first)' },
                  { name: 'tail_lines', required: false, desc: 'lines from end (default 200)' },
                ]},
                { name: 'list_namespaces', desc: 'List all Kubernetes namespaces with their status. Use to discover available namespaces before filtering other queries.', params: [] },
                { name: 'list_helm_releases', desc: 'List all Helm releases in the cluster with their status and health. Returns release name, namespace, chart, version, status, and resource health.', params: [
                  { name: 'namespace', required: false, desc: 'filter to a specific namespace' },
                ]},
                { name: 'get_helm_release', desc: 'Get detailed information about a specific Helm release including owned resources and their status. Optionally include values, revision history, or manifest diff between revisions.', params: [
                  { name: 'namespace', required: true, desc: 'release namespace' },
                  { name: 'name', required: true, desc: 'release name' },
                  { name: 'include', required: false, desc: 'values, history, diff' },
                  { name: 'diff_revision_1', required: false, desc: 'first revision for diff' },
                  { name: 'diff_revision_2', required: false, desc: 'second revision for diff (defaults to current)' },
                ]},
                { name: 'get_workload_logs', desc: 'Get aggregated, AI-filtered logs from all pods of a workload (Deployment, StatefulSet, or DaemonSet). Logs are collected from all matching pods, filtered for errors/warnings, and deduplicated.', params: [
                  { name: 'kind', required: true, desc: 'deployment, statefulset, or daemonset' },
                  { name: 'namespace', required: true, desc: 'workload namespace' },
                  { name: 'name', required: true, desc: 'workload name' },
                  { name: 'container', required: false, desc: 'specific container name' },
                  { name: 'tail_lines', required: false, desc: 'lines per pod (default 100)' },
                ]},
                { name: 'manage_workload', desc: 'Perform operations on a workload. \'restart\' triggers a rolling restart, \'scale\' changes the replica count, \'rollback\' reverts to a previous revision.', params: [
                  { name: 'action', required: true, desc: 'restart, scale, or rollback' },
                  { name: 'kind', required: true, desc: 'deployment, statefulset, or daemonset' },
                  { name: 'namespace', required: true, desc: 'workload namespace' },
                  { name: 'name', required: true, desc: 'workload name' },
                  { name: 'replicas', required: false, desc: 'target replica count (for scale)' },
                  { name: 'revision', required: false, desc: 'target revision (for rollback)' },
                ]},
                { name: 'manage_cronjob', desc: 'Perform operations on a CronJob. \'trigger\' creates a manual Job run, \'suspend\' pauses the schedule, \'resume\' re-enables it.', params: [
                  { name: 'action', required: true, desc: 'trigger, suspend, or resume' },
                  { name: 'namespace', required: true, desc: 'cronjob namespace' },
                  { name: 'name', required: true, desc: 'cronjob name' },
                ]},
                { name: 'manage_gitops', desc: 'Perform operations on GitOps resources. ArgoCD: sync, suspend, resume. FluxCD: reconcile, suspend, resume.', params: [
                  { name: 'action', required: true, desc: 'sync/reconcile, suspend, or resume' },
                  { name: 'tool', required: true, desc: 'argocd or fluxcd' },
                  { name: 'namespace', required: true, desc: 'resource namespace' },
                  { name: 'name', required: true, desc: 'resource name' },
                  { name: 'kind', required: false, desc: 'FluxCD resource kind (e.g. kustomization, helmrelease)' },
                ]},
              ].map((tool) => (
                <div key={tool.name} className="px-3 py-2 rounded bg-theme-base/50 space-y-1.5">
                  <code className="text-[11px] font-mono text-purple-400">{tool.name}</code>
                  <p className="text-[11px] text-theme-text-tertiary leading-relaxed">{tool.desc}</p>
                  {tool.params.length > 0 && (
                    <div className="flex flex-wrap gap-1.5 pt-0.5">
                      {tool.params.map((p) => (
                        <span key={p.name} className="inline-flex items-center gap-1 text-[10px] font-mono bg-theme-elevated px-1.5 py-0.5 rounded" title={p.desc}>
                          <span className="text-theme-text-secondary">{p.name}</span>
                          {p.required && <span className="text-red-400">*</span>}
                        </span>
                      ))}
                    </div>
                  )}
                </div>
              ))}
            </div>
          </div>
        </div>

        {/* Footer */}
        <div className="flex items-center justify-between px-6 py-3 border-t border-theme-border shrink-0">
          <a
            href="https://github.com/skyhook-io/radar/blob/main/docs/mcp.md"
            target="_blank"
            rel="noopener noreferrer"
            className="text-xs text-theme-text-tertiary hover:text-purple-400 transition-colors"
          >
            Documentation
          </a>
          <button
            onClick={onClose}
            className="px-4 py-2 text-sm font-medium rounded-lg hover:bg-theme-elevated transition-colors cursor-pointer text-theme-text-secondary"
          >
            Close
          </button>
        </div>
      </div>
    </div>
  )
}
