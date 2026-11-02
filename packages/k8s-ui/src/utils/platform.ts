/** True when running on a macOS / iOS platform (drives ⌘ vs Ctrl conventions). */
export function isMac(): boolean {
  return typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform)
}
