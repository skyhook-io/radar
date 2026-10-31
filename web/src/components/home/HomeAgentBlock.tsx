import { useState } from 'react'
import { Radio, Sparkles } from 'lucide-react'
import { useDiagnose } from '../diagnose/DiagnoseContext'
import { notReadyTooltip } from '../diagnose/LocalDiagnoseAction'
import { useNavCustomization } from '../../context/NavCustomization'
import { Tooltip } from '../ui/Tooltip'
import { MCPSetupDialog } from './MCPSetupDialog'

// The Home card's AI entry: start an investigation with the user's own agent,
// and connect an agent to this Radar's MCP server. The investigation line
// follows the other AI entry points: hidden where investigations can't run
// here, a setup prompt when no agent is ready.
export function HomeAgentBlock({
  mcpUrl,
  readOnly,
  execTools,
}: {
  mcpUrl: string
  readOnly?: boolean
  execTools?: boolean
}) {
  const [mcpDialogOpen, setMcpDialogOpen] = useState(false)
  const d = useDiagnose()
  const { embedded } = useNavCustomization()
  const showInvestigate = !embedded && d.setupState !== 'off' && d.setupState !== 'unknown'

  return (
    <div className="mt-3 flex flex-col gap-1.5 rounded-md border border-purple-500/20 bg-purple-500/5 p-2">
      {showInvestigate && <InvestigateLine />}
      <div className="flex min-w-0 items-center gap-1.5 text-[11px]">
        <Radio className="h-3 w-3 shrink-0 animate-pulse text-purple-600 dark:text-purple-400" aria-hidden />
        <span className="shrink-0 font-medium text-purple-600 dark:text-purple-400">
          {readOnly ? 'MCP live (read-only)' : 'MCP live'}
        </span>
        <Tooltip content={mcpUrl} wrapperClassName="min-w-0">
          <span className="truncate font-mono text-theme-text-tertiary">{mcpUrl.replace(/^https?:\/\//, '')}</span>
        </Tooltip>
        {/* "Connect your agent" doesn't fit beside the URL in this column;
            the short label keeps the endpoint readable. */}
        <button
          type="button"
          onClick={() => setMcpDialogOpen(true)}
          aria-label="Connect your agent"
          className="ml-auto shrink-0 whitespace-nowrap font-medium text-purple-600 hover:underline dark:text-purple-400"
        >
          Connect
        </button>
      </div>
      <MCPSetupDialog
        open={mcpDialogOpen}
        onClose={() => setMcpDialogOpen(false)}
        mcpUrl={mcpUrl}
        readOnly={readOnly}
        execTools={execTools}
      />
    </div>
  )
}

function InvestigateLine() {
  const d = useDiagnose()
  if (!d.available) {
    const label = d.setupState === 'needs-restart' ? 'Check agent setup' : 'Set up an agent'
    return (
      <Tooltip content={notReadyTooltip(d)} wrapperClassName="w-full">
        <button
          type="button"
          onClick={d.openHome}
          className="flex w-full items-center gap-2 rounded px-0.5 text-left text-xs font-medium text-purple-600 hover:underline dark:text-purple-400"
        >
          <Sparkles className="h-3.5 w-3.5 shrink-0" aria-hidden />
          {label}
        </button>
      </Tooltip>
    )
  }
  const where = d.hosted ? `powered by ${d.agentLabel}` : `runs your own ${d.agentLabel} locally`
  return (
    <Tooltip content={`Start an AI investigation — ${where}`} wrapperClassName="w-full">
      <button
        type="button"
        // Opens the investigations panel's question composer, focused: the
        // composer is where the scope and look-back are chosen before a run.
        onClick={() => (d.questions ? d.openQuestion({}) : d.openWorkspace())}
        className="flex w-full items-center gap-2 rounded border border-theme-border bg-theme-surface px-2 py-1.5 text-left text-xs text-theme-text-tertiary transition-colors hover:border-purple-500/40 hover:text-theme-text-secondary"
      >
        <Sparkles className="h-3.5 w-3.5 shrink-0 text-purple-600 dark:text-purple-400" aria-hidden />
        <span className="truncate">
          {d.questions ? `Ask ${d.agentLabel} about this cluster…` : `Investigate with ${d.agentLabel}`}
        </span>
      </button>
    </Tooltip>
  )
}
