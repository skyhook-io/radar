import { describe, expect, it } from 'vitest'
import { readFileSync } from 'fs'
import { join } from 'path'
import { hasCuratedColumns } from './ResourcesView'

// Parsed from the source rather than exported: this asserts a property of the
// data as it is written, and an export would let the two drift.
const SRC = readFileSync(join(__dirname, 'ResourcesView.tsx'), 'utf8')

function objectBody(declName: string): string {
  const start = SRC.indexOf(declName)
  if (start < 0) throw new Error(`${declName} not found — the checks below would pass vacuously`)
  const open = SRC.indexOf('{', start)
  let depth = 1
  let i = open + 1
  while (depth > 0) {
    if (SRC[i] === '{') depth++
    else if (SRC[i] === '}') depth--
    i++
  }
  return SRC.slice(open + 1, i)
}

function objectKeys(declName: string): string[] {
  return [...objectBody(declName).matchAll(/^ {2}([a-z0-9_]+):/gm)].map(m => m[1])
}

const CURATED = objectKeys('const KNOWN_COLUMNS')
const OWNED = new Set(objectKeys('const CURATED_COLUMN_GROUPS'))

// Curated sets keyed on a kind the API server reserves. A CRD cannot be created
// in a core group, so these can never be claimed by a foreign resource.
const CORE_KEYS = new Set([
  'pods', 'services', 'deployments', 'replicasets', 'statefulsets', 'daemonsets', 'jobs',
  'cronjobs', 'configmaps', 'secrets', 'ingresses', 'ingressclasses', 'endpoints',
  'endpointslices', 'persistentvolumeclaims', 'persistentvolumes', 'storageclasses', 'nodes',
  'namespaces', 'serviceaccounts', 'roles', 'rolebindings', 'clusterroles', 'clusterrolebindings',
  'horizontalpodautoscalers', 'hpas', 'poddisruptionbudgets', 'networkpolicies', 'events',
  'leases', 'priorityclasses', 'runtimeclasses', 'resourcequotas', 'limitranges', 'csidrivers',
  'csinodes', 'volumeattachments', 'mutatingwebhookconfigurations', 'validatingwebhookconfigurations',
  'certificatesigningrequests', 'apiservices', 'customresourcedefinitions', 'replicationcontrollers',
  'controllerrevisions', 'podtemplates', 'volumeattributesclasses', 'validatingadmissionpolicies',
  'validatingadmissionpolicybindings', 'flowschemas', 'prioritylevelconfigurations',
  'componentstatuses', 'resourceclaims', 'resourceclaimtemplates', 'resourceslices', 'deviceclasses',
])

// Groups are unbounded (one CRD per provider service), so these are gated by a
// predicate instead of an enumerated list.
const UNBOUNDED_KEYS = new Set(['providerconfigs', 'crossplanemanagedresources'])

describe('curated column ownership', () => {
  // The guard that matters: a curated set added without an owning group would
  // silently claim every CRD that happens to share its plural.
  it('declares an owning group for every non-core curated set', () => {
    const undeclared = CURATED.filter(k => !CORE_KEYS.has(k) && !UNBOUNDED_KEYS.has(k) && !OWNED.has(k))
    expect(undeclared).toEqual([])
  })

  it('does not declare ownership for a set that no longer exists', () => {
    expect([...OWNED].filter(k => !CURATED.includes(k))).toEqual([])
  })

  // Brace-counting, not parsing: assert the extraction actually saw the whole
  // table, or a silently-missed key would let the guard above pass vacuously.
  it('extracted every curated key', () => {
    expect(CURATED.length).toBe(239)
    expect(OWNED.size).toBe(201)
    expect(CURATED).toContain('pods')
    expect(CURATED).toContain('crossplanemanagedresources')
    expect([...OWNED]).toContain('awsmanagedcontrolplanes')
  })
})

describe('curated sets are reachable', () => {
  // Ownership answers "who may use this key". It does not answer "can anything
  // reach this key at all" — and a curated set nothing routes to is invisible
  // while every other check stays green. kyvernopolicies was exactly that: real
  // columns, real cells, and no normalization entry to arrive through.
  const QUALIFIED_TARGETS = new Set(
    [...objectBody('const GROUP_QUALIFIED_COLUMN_KEYS').matchAll(/'[^']*':\s*'([a-z0-9_]+)'/g)].map(m => m[1]),
  )

  // Keys named after a vendor are Radar's own invention rather than a real API
  // plural, so something has to route to them explicitly.
  const VENDOR_PREFIXES = /^(cnpg|capi|knative|calico|velero|kyverno|nvidia|crossplane|istio)/

  // Reached by predicate rather than by the qualified map.
  const PREDICATE_REACHED = new Set(['crossplanemanagedresources'])
  // Genuine upstream plurals that happen to start with a vendor name.
  const REAL_PLURALS = new Set(['nvidiadrivers'])

  it('routes something to every vendor-named curated set', () => {
    const unreachable = CURATED.filter(
      k => VENDOR_PREFIXES.test(k) &&
        !QUALIFIED_TARGETS.has(k) &&
        !PREDICATE_REACHED.has(k) &&
        !REAL_PLURALS.has(k),
    )
    expect(unreachable).toEqual([])
  })

  it('resolves the real plural to the vendor key, not just the key to itself', () => {
    // The inverse of the vacuity trap above: assert the plural a cluster would
    // actually serve, not Radar's internal name for the column set.
    expect(hasCuratedColumns('policies', 'kyverno.io')).toBe(true)
    expect(hasCuratedColumns('gateways', 'networking.istio.io')).toBe(true)
    expect(hasCuratedColumns('backups', 'postgresql.cnpg.io')).toBe(true)
    expect(hasCuratedColumns('clusters', 'cluster.x-k8s.io')).toBe(true)
    expect(hasCuratedColumns('services', 'serving.knative.dev')).toBe(true)
    // ...and that an unrelated vendor still gets nothing.
    expect(hasCuratedColumns('policies', 'someone-else.io')).toBe(false)
  })
})

describe('every declaration resolves', () => {
  // Size checks cannot catch a wrong group. Each declared pair has to actually
  // return curated columns, and the same key under a group nobody claims has to
  // not — run over all 160 rather than a hand-picked sample.
  const DECLARED = [...objectBody('const CURATED_COLUMN_GROUPS')
    .matchAll(/^ {2}([a-z0-9_]+): \[([^\]\n]+)\],$/gm)]
    .map(m => [m[1], m[2].split(',').map(g => g.trim().replace(/'/g, ''))] as const)

  it('covers the whole ownership table', () => {
    expect(DECLARED.length).toBe(201)
  })

  it.each(DECLARED)('%s is curated under each group it claims', (key, groups) => {
    for (const g of groups) expect(hasCuratedColumns(key, g)).toBe(true)
  })

  it.each(DECLARED)('%s is not curated under an unclaimed group', (key, _groups) => {
    // Deliberately not a Crossplane/upbound suffix, which is matched by predicate.
    expect(hasCuratedColumns(key, 'nobody-claims-this.invalid')).toBe(false)
  })
})

describe('foreign CRDs do not inherit curated columns', () => {
  // Each of these ships in the wild under a plural Radar curates for someone
  // else. chaos-mesh Workflows are present on real clusters today.
  it.each([
    ['workflows', 'chaos-mesh.org', 'argoproj.io'],
    ['providers', 'notification.toolkit.fluxcd.io', 'pkg.crossplane.io'],
    ['services', 'example.com', ''],
    ['backups', 'third-party.io', 'velero.io'],
    ['clusters', 'kubeblocks.io', ''],
    ['certificates', 'unrelated.io', 'cert-manager.io'],
  ])('%s.%s is not treated as curated', (plural, foreignGroup) => {
    expect(hasCuratedColumns(plural, foreignGroup)).toBe(false)
  })

  it.each([
    ['workflows', 'argoproj.io'],
    ['gateways', 'gateway.networking.k8s.io'],
    ['providers', 'pkg.crossplane.io'],
    ['scaledobjects', 'keda.sh'],
    ['certificates', 'cert-manager.io'],
    ['nodepools', 'karpenter.sh'],
    ['externalsecrets', 'external-secrets.io'],
    ['kustomizations', 'kustomize.toolkit.fluxcd.io'],
    ['virtualservices', 'networking.istio.io'],
    ['vulnerabilityreports', 'aquasecurity.github.io'],
  ])('%s.%s keeps its curated columns', (plural, group) => {
    expect(hasCuratedColumns(plural, group)).toBe(true)
  })

  // Core kinds carry no ownership entry and must not regress.
  it.each([
    ['pods', ''],
    ['deployments', 'apps'],
    ['jobs', 'batch'],
    ['ingresses', 'networking.k8s.io'],
    ['roles', 'rbac.authorization.k8s.io'],
  ])('core %s.%s stays curated', (plural, group) => {
    expect(hasCuratedColumns(plural, group)).toBe(true)
  })

  // Both are curated, but not by the same set: an Istio Gateway has servers and
  // a workload selector, a Gateway API Gateway has a class, listeners, routes
  // and addresses, and neither vocabulary describes the other.
  it('gives Istio and Gateway API their own gateway column sets', () => {
    expect(hasCuratedColumns('gateways', 'networking.istio.io')).toBe(true)
    expect(hasCuratedColumns('gateways', 'gateway.networking.k8s.io')).toBe(true)

    // objectBody slices to the first `{`, which inside an array is its first
    // element — so take the array literal itself.
    const columnKeys = (key: string) => {
      const start = SRC.indexOf(`\n  ${key}: [`)
      expect(start).toBeGreaterThan(-1)
      const end = SRC.indexOf('\n  ],', start)
      return [...SRC.slice(start, end).matchAll(/key: '([a-zA-Z]+)'/g)].map(m => m[1])
    }
    const istio = columnKeys('istiogateways')
    const gwapi = columnKeys('gateways')
    expect(istio).toContain('servers')
    expect(istio).toContain('selector')
    expect(gwapi).toContain('listeners')
    expect(istio).not.toContain('listeners')
    expect(gwapi).not.toContain('servers')
  })

  // CAPA serves AWSManagedControlPlane in the control-plane group; CAPZ and CAPG
  // serve their equivalents in the infrastructure group. Declaring AWS as
  // infrastructure silently dropped the curated columns on every CAPA cluster.
  it('claims AWSManagedControlPlane in the control-plane group', () => {
    expect(hasCuratedColumns('awsmanagedcontrolplanes', 'controlplane.cluster.x-k8s.io')).toBe(true)
    expect(hasCuratedColumns('awsmanagedcontrolplanes', 'infrastructure.cluster.x-k8s.io')).toBe(false)
    expect(hasCuratedColumns('azuremanagedcontrolplanes', 'infrastructure.cluster.x-k8s.io')).toBe(true)
    expect(hasCuratedColumns('gcpmanagedcontrolplanes', 'infrastructure.cluster.x-k8s.io')).toBe(true)
  })

  // metrics.k8s.io is an aggregated API, not a group the API server reserves,
  // and PodMetrics/NodeMetrics carry the plurals `pods`/`nodes`. Treating it as
  // core handed them the Pod and Node column sets, which read fields they lack.
  it('does not treat the aggregated metrics API as a core group', () => {
    expect(hasCuratedColumns('pods', 'metrics.k8s.io')).toBe(false)
    expect(hasCuratedColumns('nodes', 'metrics.k8s.io')).toBe(false)
    expect(hasCuratedColumns('pods', '')).toBe(true)
  })

  // A provider ships one CRD per service, so managed resources collide with
  // curated plurals constantly. The MR check has to run before the curated
  // lookup or the collision resolves to generic columns and never reaches it.
  it('keeps a managed resource whose plural collides with a curated kind', () => {
    expect(hasCuratedColumns('certificates', 'acm.aws.upbound.io')).toBe(true)
    expect(hasCuratedColumns('services', 's3.aws.upbound.io')).toBe(true)
    expect(hasCuratedColumns('certificates', 'cert-manager.io')).toBe(true)
  })

  // Crossplane ProviderConfigs have one CRD per provider, so their groups
  // cannot be enumerated and are matched by suffix instead.
  it('keeps ProviderConfig curated across unbounded provider groups', () => {
    expect(hasCuratedColumns('providerconfigs', 'aws.upbound.io')).toBe(true)
    expect(hasCuratedColumns('providerconfigs', 'kubernetes.crossplane.io')).toBe(true)
    expect(hasCuratedColumns('providerconfigs', 'not-crossplane.io')).toBe(false)
  })
})
