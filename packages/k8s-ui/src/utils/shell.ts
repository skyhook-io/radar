/** Quote one shell argument for POSIX shells and zsh. */
export function shellWord(v: string): string {
  if (!v.startsWith('=') && /^[A-Za-z0-9_.@%+=:,/-]+$/.test(v)) return v
  return `'${v.replace(/'/g, `'\\''`)}'`
}
