// Remediation commands are typed into an interactive terminal, so quoting is
// not a sufficient defense: the PTY line discipline acts on control bytes
// (^C cancels the line, newline submits it) before any shell parses the
// quotes, and the Windows cmd.exe fallback ignores POSIX quoting entirely.
// Values derived from a kubeconfig context name — which is routinely supplied
// by someone else — must therefore be checked, not escaped: anything outside
// this set means the command is not offered at all.
//
// The set covers every value real provider context names produce (cluster
// names, regions/zones, project and account IDs) and must start
// alphanumeric so a value can never lead with `-` (flag injection), `~`, or
// `=` (zsh path expansion).
const SHELL_SAFE_VALUE = /^[A-Za-z0-9][A-Za-z0-9._:@-]*$/

export function isShellSafeValue(value: string | null | undefined): value is string {
  return typeof value === 'string' && SHELL_SAFE_VALUE.test(value)
}

export function allShellSafe(...values: (string | null | undefined)[]): boolean {
  return values.every(isShellSafeValue)
}
