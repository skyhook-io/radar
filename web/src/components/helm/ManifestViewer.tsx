import { Copy, Check, Code } from 'lucide-react'
import { CodeViewer } from '../ui/CodeViewer'

interface ManifestViewerProps {
  manifest: string
  isLoading: boolean
  revision?: number
  onCopy: (text: string) => void
  copied: boolean
}

export function ManifestViewer({ manifest, isLoading, revision, onCopy, copied }: ManifestViewerProps) {
  if (isLoading) {
    return (
      <div className="flex items-center justify-center h-32 text-theme-text-tertiary">
        Loading manifest...
      </div>
    )
  }

  if (!manifest) {
    return (
      <div className="flex flex-col items-center justify-center h-32 text-theme-text-tertiary gap-2">
        <Code className="w-8 h-8 text-theme-text-disabled" />
        <span>No manifest available</span>
      </div>
    )
  }

  const lineCount = manifest.split('\n').length

  return (
    <div className="p-4">
      <div className="flex items-center justify-between mb-3">
        <div className="flex items-center gap-2">
          <span className="text-sm font-medium text-theme-text-secondary">Rendered Manifest</span>
          {revision && (
            <span className="px-2 py-0.5 text-xs bg-theme-elevated text-theme-text-secondary rounded">
              Revision {revision}
            </span>
          )}
          <span className="text-xs text-theme-text-tertiary">{lineCount} lines</span>
        </div>
        <button
          onClick={() => onCopy(manifest)}
          className="flex items-center gap-1 px-2 py-1 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
        >
          {copied ? <Check className="w-3.5 h-3.5 text-green-400" /> : <Copy className="w-3.5 h-3.5" />}
          Copy
        </button>
      </div>

      <CodeViewer
        code={manifest}
        language="yaml"
        showLineNumbers
        maxHeight="calc(100vh - 300px)"
      />
    </div>
  )
}
