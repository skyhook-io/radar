/**
 * Pure helpers for the Secret panel's reveal-confirmation flow.
 *
 * Extracted from SecretRenderer so the rule that decides
 *   "click on Reveal → ?  flip immediately, or open the confirm
 *    dialog, or close (Hide)"
 * can be unit-tested without rendering React.
 *
 * The rule (SKY-830 bug 37):
 *   - If the key is currently revealed, clicking Reveal/Hide hides
 *     it. No confirmation needed for hiding.
 *   - If the user has not yet confirmed in this panel mount,
 *     opening Reveal must open the confirmation dialog (decision
 *     "prompt") and defer the actual reveal.
 *   - Once the user has confirmed once, subsequent reveals in the
 *     same mount go through immediately (decision "reveal"). We
 *     don't re-prompt per-key because that would be hostile when
 *     inspecting a Secret with many keys.
 */

export type RevealDecision = 'hide' | 'prompt' | 'reveal'

export interface RevealState {
  /** Set of keys currently revealed in the UI. */
  revealed: ReadonlySet<string>
  /** True if the user has explicitly confirmed at least once in
   *  this panel mount. */
  hasConfirmedReveal: boolean
}

export function decideReveal(state: RevealState, key: string): RevealDecision {
  if (state.revealed.has(key)) return 'hide'
  if (!state.hasConfirmedReveal) return 'prompt'
  return 'reveal'
}
