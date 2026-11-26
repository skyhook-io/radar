// Shaped like a real policy whose Policy Flow was unreadable: four ingress
// rules, one of them with 261 ipBlock peers.
export const nameIn = (...values: string[]) => ({
  matchExpressions: [{ key: 'kubernetes.io/metadata.name', operator: 'In', values }],
})

export const SYSTEM_NAMESPACES = [
  'kube-system', 'monitoring', 'logging', 'cattle-system', 'fleet-system', 'cert-manager',
  'cnpg-system', 'kueue-system', 'longhorn-system', 'flux-system', 'platform-system',
  'gateways', 'db-tools', 'pgadmin',
]

export const LARGE_POLICY_CIDRS = [
  ...['10', '12', '20', '30', '40'].map((o) => `192.168.${o}.0/24`),
  ...Array.from({ length: 256 }, (_, i) => `10.42.${i}.0/32`),
]

export const largePolicySpec = {
  podSelector: {},
  policyTypes: ['Ingress'],
  ingress: [
    { from: [{ podSelector: {} }] },
    { from: [{ namespaceSelector: nameIn(...SYSTEM_NAMESPACES) }] },
    { from: LARGE_POLICY_CIDRS.map((cidr) => ({ ipBlock: { cidr } })) },
    { from: [{ namespaceSelector: nameIn('analytics') }], ports: [{ port: 5432, protocol: 'TCP' }] },
  ],
}
