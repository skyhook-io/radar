import { clsx } from 'clsx'

// Shared unified-diff rendering primitives. Every diff surface (Helm manifest
// diff, Helm values preview, revision history, Argo resource diff) classifies
// lines by the same leading marker; keeping that logic here stops the four
// renderers from silently drifting apart.

// classifyDiffLine tags a unified-diff line as an addition, a removal, or a hunk/
// file header. `+++`/`---` file headers are NOT additions/removals.
export function classifyDiffLine(line: string): {
  isAddition: boolean
  isRemoval: boolean
  isHeader: boolean
} {
  return {
    isAddition: line.startsWith('+') && !line.startsWith('+++'),
    isRemoval: line.startsWith('-') && !line.startsWith('---'),
    isHeader: line.startsWith('@@') || line.startsWith('---') || line.startsWith('+++'),
  }
}

// hasDiffBodyChange is true when a unified-diff string has any added/removed body
// line (ignoring the patch's own file/hunk headers) — the "any real change?"
// short-circuit shared by the manifest and Argo diff surfaces.
export function hasDiffBodyChange(diff: string): boolean {
  return diff.split('\n').some((line) => {
    const { isAddition, isRemoval } = classifyDiffLine(line)
    return isAddition || isRemoval
  })
}

// DiffLine renders one unified-diff line colored by marker — the gutter-less
// "manifest" style shared by the Helm manifest diff and the Argo resource diff.
// (The line-numbered gutter variants build their own row but share
// classifyDiffLine.)
export function DiffLine({ line }: { line: string }) {
  const { isAddition, isRemoval, isHeader } = classifyDiffLine(line)
  return (
    <div
      className={clsx(
        'whitespace-pre',
        isAddition && 'bg-green-500/10 text-green-700 dark:text-green-400',
        isRemoval && 'bg-red-500/10 text-red-700 dark:text-red-400',
        isHeader && 'text-theme-text-tertiary font-bold',
        !isAddition && !isRemoval && !isHeader && 'text-theme-text-secondary'
      )}
    >
      {line || ' '}
    </div>
  )
}
