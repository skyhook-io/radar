import { describe, it, expect } from 'vitest'
import { englishPlural, englishSingular, isEnglishPlural, pluralNoun, pluralize } from './pluralize'

describe('englishPlural', () => {
  describe('default +s', () => {
    it('handles common nouns', () => {
      expect(englishPlural('cluster')).toBe('clusters')
      expect(englishPlural('cert')).toBe('certs')
      expect(englishPlural('chart')).toBe('charts')
    })
  })

  describe('s/x/ch/sh → +es', () => {
    it('handles s ending', () => {
      expect(englishPlural('class')).toBe('classes')
      expect(englishPlural('ingress')).toBe('ingresses')
    })

    it('handles x ending', () => {
      expect(englishPlural('box')).toBe('boxes')
    })

    it('handles ch ending', () => {
      expect(englishPlural('match')).toBe('matches')
    })

    it('handles sh ending', () => {
      expect(englishPlural('brush')).toBe('brushes')
    })
  })

  describe('consonant-y → -y +ies', () => {
    it('handles policy', () => {
      expect(englishPlural('policy')).toBe('policies')
    })

    it('handles directory', () => {
      expect(englishPlural('directory')).toBe('directories')
    })

    it('preserves vowel-y → +s (does not strip)', () => {
      expect(englishPlural('day')).toBe('days')
      expect(englishPlural('boy')).toBe('boys')
    })
  })

  describe('case insensitivity in detection', () => {
    // The detection regex is case-insensitive (recognizes Class as ending
    // in 's'-class, POLICY as consonant-y) but the rule appends lowercase
    // suffixes. In practice all callers normalize to lowercase before
    // calling — kindToPlural() lowercases its input; pluralize() takes
    // user-controlled nouns which are typically lowercase.
    it('detects rule-applicable suffixes regardless of case', () => {
      expect(englishPlural('Class')).toBe('Classes')   // +es appended lowercase
      expect(englishPlural('Box')).toBe('Boxes')
    })
  })
})

describe('pluralNoun', () => {
  it('returns singular for n===1', () => {
    expect(pluralNoun(1, 'cluster')).toBe('cluster')
  })

  it('returns plural for n!==1', () => {
    expect(pluralNoun(0, 'cluster')).toBe('clusters')
    expect(pluralNoun(2, 'cluster')).toBe('clusters')
    expect(pluralNoun(99, 'cluster')).toBe('clusters')
  })

  it('uses englishPlural rules by default', () => {
    expect(pluralNoun(2, 'policy')).toBe('policies')
    expect(pluralNoun(2, 'class')).toBe('classes')
  })

  it('honors explicit override for irregular plurals', () => {
    expect(pluralNoun(2, 'index', 'indices')).toBe('indices')
    expect(pluralNoun(1, 'index', 'indices')).toBe('index')
  })
})

describe('pluralize', () => {
  it('formats count + noun', () => {
    expect(pluralize(0, 'cluster')).toBe('0 clusters')
    expect(pluralize(1, 'cluster')).toBe('1 cluster')
    expect(pluralize(5, 'cluster')).toBe('5 clusters')
  })

  it('handles irregular plurals via override', () => {
    expect(pluralize(3, 'index', 'indices')).toBe('3 indices')
  })

  it('applies English rules without override', () => {
    expect(pluralize(2, 'policy')).toBe('2 policies')
    expect(pluralize(2, 'class')).toBe('2 classes')
    expect(pluralize(2, 'box')).toBe('2 boxes')
  })
})

describe('englishSingular', () => {
  it('inverts englishPlural for regular plurals', () => {
    expect(englishSingular('clusters')).toBe('cluster')
    expect(englishSingular('policies')).toBe('policy')
    expect(englishSingular('classes')).toBe('class')
    expect(englishSingular('boxes')).toBe('box')
    expect(englishSingular('batches')).toBe('batch')
  })

  it('leaves nouns with no plural ending alone', () => {
    expect(englishSingular('cluster')).toBe('cluster')
    expect(englishSingular('policy')).toBe('policy')
  })

  // The `-es` strip over-shortens a `*se` singular ('leases' → 'leas', not
  // 'lease'), but englishPlural's `s → +es` rule inverts it exactly, so the
  // round trip these two functions are used for still holds. Only the round
  // trip is contractual — the intermediate stem is not.
  it('over-strips *se stems but stays round-trip exact', () => {
    expect(englishSingular('leases')).toBe('leas')
    expect(englishPlural('leas')).toBe('leases')
    expect(englishSingular('helmreleases')).toBe('helmreleas')
    expect(englishPlural('helmreleas')).toBe('helmreleases')
  })
})

describe('isEnglishPlural', () => {
  it('recognizes regular plurals', () => {
    expect(isEnglishPlural('schedules')).toBe(true)
    expect(isEnglishPlural('validatingpolicies')).toBe(true)
    expect(isEnglishPlural('virtualservices')).toBe(true)
    expect(isEnglishPlural('gatewayclasses')).toBe(true)
    expect(isEnglishPlural('endpoints')).toBe(true)
  })

  // Plurals whose singular ends in `se`. The stem englishSingular produces is
  // wrong, but the round trip is not — see the englishSingular case above.
  it('recognizes plurals of *se singulars', () => {
    expect(isEnglishPlural('leases')).toBe(true)
    expect(isEnglishPlural('helmreleases')).toBe(true)
    expect(isEnglishPlural('releases')).toBe(true)
    expect(isEnglishPlural('licenses')).toBe(true)
    expect(isEnglishPlural('databases')).toBe(true)
  })

  // KNOWN LIMIT. A singular ending in a lone `s` whose stem doesn't end in
  // s/x/ch/sh is indistinguishable from a plural by English rules alone
  // ('status' → 'statu' → 'status' round-trips exactly like 'clusters' →
  // 'cluster' → 'clusters'). No Kubernetes Kind Radar handles has this shape,
  // and kindToPlural's discovered-map lookup takes over the moment discovery
  // lands, so the mistake is confined to the cold window. Pinned so a future
  // change to these rules surfaces the boundary rather than moving it silently.
  it('cannot distinguish singulars ending in a lone s', () => {
    expect(isEnglishPlural('status')).toBe(true)
    expect(isEnglishPlural('alias')).toBe(true)
    expect(isEnglishPlural('canvas')).toBe(true)
  })

  // The reason this predicate exists rather than a bare endsWith('s'): these
  // are singular Kubernetes kinds that happen to end in 's'.
  it('rejects singulars that merely end in s', () => {
    expect(isEnglishPlural('ingress')).toBe(false)
    expect(isEnglishPlural('nodeclass')).toBe(false)
    expect(isEnglishPlural('ec2nodeclass')).toBe(false)
    expect(isEnglishPlural('storageclass')).toBe(false)
  })

  it('rejects singulars with no plural ending', () => {
    expect(isEnglishPlural('cluster')).toBe(false)
    expect(isEnglishPlural('validatingpolicy')).toBe(false)
  })

  it('agrees with englishPlural on every round trip', () => {
    for (const singular of ['cluster', 'policy', 'class', 'box', 'batch', 'schedule', 'service']) {
      expect(isEnglishPlural(englishPlural(singular))).toBe(true)
    }
  })
})
