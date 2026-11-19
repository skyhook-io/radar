/** v as one POSIX shell word: bare when it holds only safe characters, else single-quoted. */
export function shellWord(v: string): string {
  return /^[A-Za-z0-9_.@%+=:,/-]+$/.test(v) ? v : `'${v.replace(/'/g, `'\\''`)}'`
}
