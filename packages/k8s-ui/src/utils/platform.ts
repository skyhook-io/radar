/** True when running on a macOS / iOS platform (drives ⌘ vs Ctrl conventions). */
export function isMac(): boolean {
  return typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform)
}

/** True when the browser runs on Windows. Radar's local surfaces run on the
 *  same machine, so this is also the OS of its local terminal. */
export function isWindows(): boolean {
  return typeof navigator !== 'undefined' && /^Win/.test(navigator.platform)
}
