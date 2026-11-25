import { englishPlural, isEnglishPlural } from '@skyhook-io/k8s-ui/utils/pluralize'
import type { EmptyNamespaceHint as Hint } from './namespaceHint'

interface EmptyNamespaceHintProps {
  hint: Hint
  namespaces: string[]
  /** Real Kind, e.g. "Pod". */
  kind: string
  onShowAll?: () => void
}

export function EmptyNamespaceHint({ hint, namespaces, kind, onShowAll }: EmptyNamespaceHintProps) {
  const scope = namespaces.length === 1 ? namespaces[0] : `${namespaces.length} namespaces`
  if (hint.type === 'none-elsewhere') {
    return (
      <p className="text-sm mt-1 text-theme-text-disabled">
        Filtered to {scope}. None in other namespaces either.
      </p>
    )
  }
  const noun = hint.count === 1 ? kind : isEnglishPlural(kind) ? kind : englishPlural(kind)
  return (
    <div className="mt-3 flex flex-col items-center gap-2 text-sm">
      <p className="text-theme-text-secondary">
        Filtered to {scope}.{' '}
        <span className="font-medium text-theme-text-primary">
          {hint.atLeast ? 'At least ' : ''}{hint.count.toLocaleString()} {noun}
        </span>{' '}
        {hint.count === 1 ? 'is' : 'are'} in other namespaces.
      </p>
      {onShowAll && (
        <button
          type="button"
          onClick={onShowAll}
          className="px-3 py-1.5 rounded-md text-sm font-medium bg-accent-muted text-accent-text hover:bg-theme-hover transition-colors"
        >
          Show all namespaces
        </button>
      )}
    </div>
  )
}
