import { createContext, useContext } from 'react'
import type { ReactNode } from 'react'

/**
 * A note from the host about how far back the timeline goes. The host owns
 * the wording and decides when there is one. Standalone Radar serves
 * everything it holds and never sets it.
 */
const TimelineRetentionNoteContext = createContext<ReactNode>(undefined)

export const TimelineRetentionNoteProvider = TimelineRetentionNoteContext.Provider

export function useTimelineRetentionNote(): ReactNode {
  return useContext(TimelineRetentionNoteContext)
}
