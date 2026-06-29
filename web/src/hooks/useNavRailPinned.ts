import { useCallback, useEffect, useState } from 'react'

// Pin state for the primary left nav rail.
//
// "Pinned" = the labeled, full-width sidebar; "unpinned" = the slim 56px
// icon rail with hover fly-out labels. This is a single committed choice
// the user makes once (default pinned), persisted to localStorage — NOT a
// per-route auto-collapse. A rail whose width changes as you navigate is
// disorienting; the rail is the stable anchor. A user who lives in the
// wide table views (Resources, GitOps) unpins once and keeps their width;
// fly-out labels + the ⌘K palette + `g`-mnemonic shortcuts cover the collapsed
// state's discoverability.

const STORAGE_KEY = 'radar.navRail.pinned'

function readInitial(): boolean {
  if (typeof window === 'undefined') return true
  // `localStorage` access can throw (SecurityError) when storage is denied —
  // sandboxed embeds, some privacy modes. Default to pinned (expanded), the
  // friendlier first-run state for a non-k8s-expert; power users unpin once.
  try {
    return window.localStorage.getItem(STORAGE_KEY) !== 'false'
  } catch {
    return true
  }
}

export function useNavRailPinned(): {
  pinned: boolean
  togglePinned: () => void
} {
  const [pinned, setPinned] = useState(readInitial)

  useEffect(() => {
    try {
      window.localStorage.setItem(STORAGE_KEY, String(pinned))
    } catch {
      // Private-mode / quota failures shouldn't break nav — the in-memory
      // state still drives this session; we just don't persist it.
    }
  }, [pinned])

  const togglePinned = useCallback(() => setPinned((p) => !p), [])

  return { pinned, togglePinned }
}
