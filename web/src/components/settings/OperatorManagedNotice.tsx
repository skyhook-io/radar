import { Lock } from 'lucide-react'

export function OperatorManagedNotice({ helmValue }: { helmValue?: string }) {
  return (
    <div className="flex items-start gap-2 rounded-lg border border-theme-border bg-theme-elevated/50 p-3 text-sm">
      <Lock className="mt-0.5 h-4 w-4 shrink-0 text-theme-text-tertiary" />
      <div className="min-w-0">
        <p className="font-medium text-theme-text-primary">Managed by installation</p>
        <p className="mt-1 text-xs text-theme-text-secondary">
          Ask your operator to update {helmValue ? <><code className="font-mono">{helmValue}</code> in </> : null}
          Helm values or startup configuration.
          {' '}<a className="text-accent hover:underline" href="https://github.com/skyhook-io/radar/blob/main/docs/in-cluster.md#installation-settings" target="_blank" rel="noreferrer">Configuration guide</a>
        </p>
      </div>
    </div>
  )
}
