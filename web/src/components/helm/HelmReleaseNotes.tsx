import { Check, Copy } from 'lucide-react'
import { stripAnsi } from '@skyhook-io/k8s-ui'

export function HelmReleaseNotes({ notes, copied, onCopy }: {
  notes: string
  copied: boolean
  onCopy: (text: string) => void
}) {
  const text = stripAnsi(notes)

  return (
    <div className="bg-theme-elevated/30 rounded-lg p-4">
      <div className="flex items-center justify-between mb-2">
        <h3 className="text-sm font-medium text-theme-text-secondary">Release Notes</h3>
        <button
          onClick={() => onCopy(text)}
          className="flex items-center gap-1 px-2 py-1 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
        >
          {copied ? <Check className="w-3.5 h-3.5 text-green-400" /> : <Copy className="w-3.5 h-3.5" />}
          Copy
        </button>
      </div>
      <pre className="text-xs font-mono whitespace-pre text-theme-text-secondary bg-theme-base/50 rounded p-3 max-h-64 overflow-auto">{text}</pre>
    </div>
  )
}
