// Storage can throw (SecurityError) where it is denied: sandboxed embeds, some
// privacy modes. A hint must still render then, and a dismissal must still hold
// until the page reloads, so writes also land in memory.
const memory = new Map<string, string>()

export function safeGet(storage: () => Storage, key: string): string | null {
  try {
    return storage().getItem(key) ?? memory.get(key) ?? null
  } catch {
    return memory.get(key) ?? null
  }
}

export function safeSet(storage: () => Storage, key: string, value: string) {
  memory.set(key, value)
  try {
    storage().setItem(key, value)
  } catch {
    // Storage denied: the in-memory copy carries it until reload.
  }
}

// Tests share one module instance across cases.
export function clearHintMemory() {
  memory.clear()
}

export const local = () => window.localStorage
export const session = () => window.sessionStorage
