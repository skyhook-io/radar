// Pluralization helpers shared across the package. Layered:
//
//   englishPlural(noun)         — lowest level. Applies regular English
//                                 rules: s/x/ch/sh → +es; consonant-y → ies;
//                                 default +s.
//   pluralNoun(n, sing, plural?) — count-aware plural form (just the noun).
//                                 Returns sing when n===1, else plural
//                                 (or englishPlural(sing) if not specified).
//   pluralize(n, sing, plural?)  — count + plural noun phrase. "1 cluster"
//                                 / "5 clusters" / "0 issues".
//
// kindToPlural() in navigation.ts also delegates to englishPlural for its
// English fallback, so both call paths share rule changes automatically.

/**
 * Apply English regular-pluralization rules to a noun.
 *
 *   englishPlural('class')  → 'classes'   (s/x/ch/sh → +es)
 *   englishPlural('policy') → 'policies'  (consonant-y → -y +ies)
 *   englishPlural('boy')    → 'boys'      (vowel-y → +s)
 *   englishPlural('cluster')→ 'clusters'  (default +s)
 *
 * Case-insensitive matching but preserves input case. Doesn't handle
 * irregular plurals (child/children, mouse/mice) — callers should pass
 * an explicit override for those.
 */
export function englishPlural(noun: string): string {
  if (/(?:s|x|ch|sh)$/i.test(noun)) return noun + 'es';
  if (/[^aeiou]y$/i.test(noun)) return noun.slice(0, -1) + 'ies';
  return noun + 's';
}

/**
 * Undo englishPlural's regular rules.
 *
 *   englishSingular('classes')  → 'class'
 *   englishSingular('policies') → 'policy'
 *   englishSingular('clusters') → 'cluster'
 *
 * Lossy by construction — a noun that is already singular comes back mangled
 * ('ingress' → 'ingres'). Use isEnglishPlural to find out which case you have.
 */
export function englishSingular(noun: string): string {
  const lower = noun.toLowerCase();
  if (lower.endsWith('ies')) return noun.slice(0, -3) + 'y';
  if (lower.endsWith('ses') || lower.endsWith('xes') || lower.endsWith('ches') || lower.endsWith('shes')) {
    return noun.slice(0, -2);
  }
  if (lower.endsWith('s')) return noun.slice(0, -1);
  return noun;
}

/**
 * Whether a noun is already the regular English plural of its own singular —
 * i.e. whether englishPlural would reproduce it. Distinguishes a plural from a
 * singular that merely ends in 's':
 *
 *   isEnglishPlural('schedules') → true   ('schedule' → 'schedules')
 *   isEnglishPlural('policies')  → true   ('policy'   → 'policies')
 *   isEnglishPlural('ingress')   → false  ('ingres'   → 'ingreses')
 *   isEnglishPlural('nodeclass') → false  ('nodeclas' → 'nodeclases')
 */
export function isEnglishPlural(noun: string): boolean {
  return englishPlural(englishSingular(noun)) === noun;
}

/**
 * Count-aware plural noun. Returns the singular when n===1, else the
 * plural form (passed-in or derived via englishPlural).
 *
 *   pluralNoun(1, 'cluster') → 'cluster'
 *   pluralNoun(5, 'cluster') → 'clusters'
 *   pluralNoun(0, 'cluster') → 'clusters'   (zero is plural in English)
 *   pluralNoun(2, 'index', 'indices') → 'indices'  (irregular override)
 */
export function pluralNoun(n: number, singular: string, plural?: string): string {
  return n === 1 ? singular : (plural ?? englishPlural(singular));
}

/**
 * Format a count + noun phrase with proper agreement.
 *
 *   pluralize(1, 'cluster') → '1 cluster'
 *   pluralize(5, 'cluster') → '5 clusters'
 *   pluralize(0, 'issue')   → '0 issues'
 *   pluralize(3, 'index', 'indices') → '3 indices'
 *
 * For Kubernetes resource kinds with structured plural mappings (e.g.
 * NetworkPolicy → networkpolicies), use kindToPlural() from
 * navigation.ts to produce the noun, then pass it as the plural override.
 */
export function pluralize(n: number, singular: string, plural?: string): string {
  return `${n} ${pluralNoun(n, singular, plural)}`;
}
