/**
 * Per-tab strings in sessionStorage: they survive a reload and are copied
 * when the tab is duplicated, while a tab opened from scratch starts empty.
 * Without sessionStorage (private mode, blocked site data) they live in
 * memory, so a reload forgets them.
 */
export function createTabStorage() {
  let session: Storage | undefined
  try {
    session = sessionStorage
    session.getItem('')
  } catch {
    session = undefined
  }
  const memory = new Map<string, string>()

  return {
    get(key: string): string | undefined {
      return session ? session.getItem(key) ?? undefined : memory.get(key)
    },
    set(key: string, value: string) {
      if (!session) {
        memory.set(key, value)
        return
      }
      try {
        session.setItem(key, value)
      } catch { /* over quota: keep the previous value */ }
    },
    keys(): string[] {
      if (!session) return [...memory.keys()]
      const keys: string[] = []
      for (let i = 0; i < session.length; i++) {
        const key = session.key(i)
        if (key !== null) keys.push(key)
      }
      return keys
    },
  }
}
