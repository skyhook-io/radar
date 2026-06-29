import { describe, it, expect } from 'vitest'
import { readdirSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

// Ratchet guard for the badge token system (see DESIGN.md "Badges").
//
// Hand-rolled chip color strings (`bg-<color>-N/NN text-<color>-N`) bypass the
// theme and overload hues across intents. The migration to <Badge> is
// incremental: this test pins the set of renderer files that STILL contain
// hand-rolled chips. The set may SHRINK (clean a file → remove it here) but must
// never GROW — a new renderer must use <Badge>/tokens, not literal colors.
//
// When this fails because you cleaned a file: delete it from BASELINE.
// When it fails because you added a chip: use <Badge severity|kind|protocol|tone>
// instead (DESIGN.md has the decision tree).

const CHIP_RE =
  /bg-(red|green|blue|yellow|orange|amber|purple|cyan|pink|indigo|emerald|teal|violet|rose|lime|sky)-[0-9]+\/[0-9]+ text-(red|green|blue|yellow|orange|amber|purple|cyan|pink|indigo|emerald|teal|violet|rose|lime|sky)-[0-9]+/

// Renderer files that still contain hand-rolled chips, pending migration.
// Ratchet: this list only shrinks. Do not add to it.
const BASELINE = new Set<string>([
  'AlertRenderer.tsx', 'ArgoApplicationRenderer.tsx', 'CertificateRenderer.tsx',
  'CertificateRequestRenderer.tsx', 'ChallengeRenderer.tsx', 'CiliumNetworkPolicyRenderer.tsx',
  'ClusterExternalSecretRenderer.tsx', 'ClusterIssuerRenderer.tsx', 'ClusterNetworkPolicyRenderer.tsx',
  'cnpg-cells.tsx', 'CNPGClusterRenderer.tsx', 'EventRenderer.tsx', 'GatewayRenderer.tsx',
  'GenericRenderer.tsx', 'IngressClassRenderer.tsx', 'IstioPeerAuthenticationRenderer.tsx',
  'IstioServiceEntryRenderer.tsx', 'KarpenterNodeClaimRenderer.tsx', 'knative-cells.tsx',
  'KnativeEventingRenderer.tsx', 'KnativeRevisionRenderer.tsx', 'kyverno-cells.tsx',
  'KyvernoPolicyReportRenderer.tsx', 'NetworkPolicyRenderer.tsx', 'NodeRenderer.tsx',
  'OrderRenderer.tsx', 'PodRenderer.tsx', 'RoleRenderer.tsx', 'RolloutRenderer.tsx',
  'SealedSecretRenderer.tsx', 'SecretRenderer.tsx', 'SecretStoreRenderer.tsx',
  'VeleroBackupRenderer.tsx', 'VeleroRestoreRenderer.tsx', 'VeleroScheduleRenderer.tsx',
  'VPARenderer.tsx', 'VulnerabilityReportRenderer.tsx', 'WebhookConfigRenderer.tsx',
])

const dir = fileURLToPath(new URL('.', import.meta.url))

function filesWithHandRolledChips(): string[] {
  return readdirSync(dir)
    .filter((f) => f.endsWith('.tsx') && !f.endsWith('.test.tsx'))
    .filter((f) => CHIP_RE.test(readFileSync(`${dir}/${f}`, 'utf8')))
    .sort()
}

describe('badge token system — no hand-rolled chip colors', () => {
  const offenders = filesWithHandRolledChips()

  it('does not introduce hand-rolled chips in new/migrated files (ratchet only shrinks)', () => {
    const newOffenders = offenders.filter((f) => !BASELINE.has(f))
    expect(
      newOffenders,
      `These renderers use hand-rolled chip colors. Use <Badge severity|kind|protocol|tone> ` +
        `(see DESIGN.md "Badges"), not literal bg-/text- color strings:\n  ${newOffenders.join('\n  ')}`,
    ).toEqual([])
  })

  it('baseline has no stale entries (remove files you have already cleaned)', () => {
    const stale = [...BASELINE].filter((f) => !offenders.includes(f)).sort()
    expect(
      stale,
      `These files are in BASELINE but no longer contain hand-rolled chips — ` +
        `delete them from BASELINE in this test:\n  ${stale.join('\n  ')}`,
    ).toEqual([])
  })
})
