import { useState } from 'react'
import { createTwoFilesPatch } from 'diff'
import { clsx } from 'clsx'
import { Code, GitCompare } from 'lucide-react'
import { classifyDiffLine } from './UnifiedDiff'

function stripAutoLabels(templateYaml: string): string {
  return templateYaml
    .split('\n')
    .filter(line => !line.match(/^\s+pod-template-hash:/))
    .join('\n')
}

export function RevisionDiffView({ baseTemplate, targetTemplate, baseLabel, targetLabel }: {
  baseTemplate: string
  targetTemplate: string
  baseLabel: string
  targetLabel: string
}) {
  const [expanded, setExpanded] = useState(false)

  const cleanBase = stripAutoLabels(baseTemplate)
  const cleanTarget = stripAutoLabels(targetTemplate)

  const patch = createTwoFilesPatch(
    baseLabel,
    targetLabel,
    cleanBase,
    cleanTarget,
    '', '',
    expanded ? { context: 999999 } : { context: 3 }
  )

  const lines = patch.split('\n')
  const diffLines = lines.filter(line =>
    !line.startsWith('===') && !line.startsWith('Index:')
  )

  const hasChanges = diffLines.some(l => (l.startsWith('+') && !l.startsWith('+++')) || (l.startsWith('-') && !l.startsWith('---')))

  return (
    <div className="border-t border-theme-border flex flex-col shrink-0">
      <div className="flex items-center justify-between px-4 py-2 bg-theme-elevated/50 text-xs text-theme-text-secondary shrink-0">
        <div className="flex items-center gap-4">
          <span className="flex items-center gap-1">
            <span className="w-3 h-3 bg-red-500/20 border border-red-500/50 rounded" /> {baseLabel}
          </span>
          <span className="flex items-center gap-1">
            <span className="w-3 h-3 bg-green-500/20 border border-green-500/50 rounded" /> {targetLabel}
          </span>
        </div>
        {hasChanges && (
          <button
            onClick={() => setExpanded(!expanded)}
            className="flex items-center gap-1 px-2 py-0.5 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded transition-colors"
          >
            <Code className="w-3 h-3" />
            {expanded ? 'Show changes only' : 'Show full spec'}
          </button>
        )}
      </div>
      <div className="overflow-auto max-h-[400px]">
        {hasChanges ? (
          <pre className="text-xs font-mono p-0 m-0">
            {diffLines.map((line, index) => {
              const { isAddition, isRemoval: isDeletion, isHeader } = classifyDiffLine(line)

              return (
                <div
                  key={index}
                  className={clsx(
                    'flex',
                    isAddition && 'bg-green-500/10',
                    isDeletion && 'bg-red-500/10',
                    isHeader && 'bg-blue-500/10'
                  )}
                >
                  <span className="w-10 shrink-0 text-right pr-2 py-0.5 text-theme-text-disabled select-none border-r border-theme-border/50">
                    {index + 1}
                  </span>
                  <span
                    className={clsx(
                      'flex-1 px-3 py-0.5 whitespace-pre',
                      isAddition && 'text-green-400',
                      isDeletion && 'text-red-400',
                      isHeader && 'text-blue-400 font-medium',
                      !isAddition && !isDeletion && !isHeader && 'text-theme-text-secondary'
                    )}
                  >
                    {line || ' '}
                  </span>
                </div>
              )
            })}
          </pre>
        ) : (
          <div className="flex flex-col items-center justify-center py-12 text-theme-text-tertiary">
            <GitCompare className="w-8 h-8 mb-2 text-theme-text-disabled" />
            <span className="text-sm">Templates are identical</span>
          </div>
        )}
      </div>
    </div>
  )
}
