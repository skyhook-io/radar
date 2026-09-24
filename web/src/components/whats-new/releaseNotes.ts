import type { LucideIcon } from 'lucide-react'

export interface ReleaseHighlight {
  id: string
  icon: LucideIcon
  title: string
  description: string
  /** In-app route the highlight's call to action opens. */
  path?: string
  cta?: string
}

export interface ReleaseNotes {
  version: string
  /** The first highlight leads the dialog at full width. */
  highlights: ReleaseHighlight[]
  improvements: string[]
  releaseUrl: string
}

// One entry per released version, added in that release's PR. A version with
// no entry shows neither the dialog nor the Home link.
export const RELEASE_NOTES: ReleaseNotes[] = []

export function releaseNotesFor(
  version: string | undefined,
  catalog: ReleaseNotes[] = RELEASE_NOTES,
): ReleaseNotes | undefined {
  if (!version) return undefined
  const normalized = version.startsWith('v') ? version : `v${version}`
  return catalog.find(notes => notes.version === normalized)
}
