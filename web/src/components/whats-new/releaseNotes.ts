import type { LucideIcon } from 'lucide-react'
import { compareVersions } from '../../utils/version'

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

// One entry per release that has something to announce. A version without its
// own entry shows the newest entry at or below it, so a patch upgrade (or one
// that skips the release with notes) still gets them.
export const RELEASE_NOTES: ReleaseNotes[] = []

export function releaseNotesFor(
  version: string | undefined,
  catalog: ReleaseNotes[] = RELEASE_NOTES,
): ReleaseNotes | undefined {
  if (!version) return undefined
  const normalized = version.startsWith('v') ? version : `v${version}`
  return catalog.find(notes => notes.version === normalized)
}

/** The newest notes for a release at or below `version` — what that version's user has access to. */
export function latestReleaseNotesFor(
  version: string | undefined,
  catalog: ReleaseNotes[] = RELEASE_NOTES,
): ReleaseNotes | undefined {
  if (!version) return undefined
  let latest: ReleaseNotes | undefined
  for (const notes of catalog) {
    const atOrBelow = compareVersions(notes.version, version)
    if (atOrBelow === null || atOrBelow > 0) continue
    if (!latest || (compareVersions(notes.version, latest.version) ?? 0) > 0) latest = notes
  }
  return latest
}
