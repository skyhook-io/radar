export interface ParsedContextName {
  provider: 'GKE' | 'EKS' | 'AKS' | null
  account: string | null // Project (GCP) or Account ID (AWS) or Resource Group (Azure)
  region: string | null
  clusterName: string
  raw: string // Original context name
}

/**
 * Parse a kubeconfig context name to extract cloud provider, account, region,
 * and cluster name. Used by the context switcher and error views.
 */
export function parseContextName(name: string): ParsedContextName {
  // GKE format: gke_{project}_{region|zone}_{cluster-name}
  // Region is `<continent>-<direction><number>` (e.g. us-east1, europe-west1,
  // asia-northeast1). Zonal clusters append `-<letter>` (e.g. us-east1-b).
  // We require the middle segment to contain at least one digit; that rules
  // out garbage like `gke_a_b_c` and `gke_proj_notazone_cluster` while
  // matching all real GCP zone shapes without enumerating them.
  //
  // Linear-time form: a non-consuming lookahead asserts "this segment
  // contains a digit" up to the next `_`, then a single greedy capture
  // consumes the segment. The earlier `[a-z0-9-]*\d[a-z0-9-]*` shape had
  // overlapping repeats around the digit, which CodeQL flagged as a
  // polynomial backtracking risk.
  const gkeMatch = name.match(/^gke_([a-z][a-z0-9-]+)_(?=[a-z0-9-]*\d)([a-z][a-z0-9-]*)_(.+)$/)
  if (gkeMatch) {
    const [, project, region, cluster] = gkeMatch
    return {
      provider: 'GKE',
      account: project,
      region,
      clusterName: cluster,
      raw: name,
    }
  }

  // EKS ARN format: arn:aws:eks:{region}:{account}:cluster/{cluster-name}
  const eksArnMatch = name.match(/^arn:aws:eks:([^:]+):(\d+):cluster\/(.+)$/)
  if (eksArnMatch) {
    const [, region, account, cluster] = eksArnMatch
    return {
      provider: 'EKS',
      account,
      region,
      clusterName: cluster,
      raw: name,
    }
  }

  // eksctl format: {user}@{cluster}.{region}.eksctl.io
  const eksctlMatch = name.match(/^(.+)@([^.]+)\.([^.]+)\.eksctl\.io$/)
  if (eksctlMatch) {
    const [, , cluster, region] = eksctlMatch
    return {
      provider: 'EKS',
      account: 'eksctl',
      region,
      clusterName: cluster,
      raw: name,
    }
  }

  // AKS context format produced by `az aks get-credentials`:
  //   clusterUser_<resourceGroup>_<clusterName>
  //   clusterAdmin_<resourceGroup>_<clusterName>
  // Strict regex — the prior heuristic (substring match for "aks") was a
  // correctness bug that mis-tagged user-named clusters like "tracks-prod"
  // (contains "aks") or "my-aks-experiment". Better to fall through to
  // unknown than to mis-parse.
  const aksMatch = name.match(/^cluster(?:User|Admin)_([^_]+)_(.+)$/)
  if (aksMatch) {
    const [, resourceGroup, cluster] = aksMatch
    return {
      provider: 'AKS',
      account: resourceGroup,
      region: null,
      clusterName: cluster,
      raw: name,
    }
  }

  // OpenShift `oc login` context: <project>/<server host>:<port>/<user>, where
  // oc writes the host with dots turned into dashes (api.prod.example.com ->
  // api-prod-example-com) and keeps an IPv6 host in brackets. The user may
  // itself contain `:` or `/` (kube:admin, system:admin). The host is the
  // only part that names the cluster: `oc project` writes a new context per
  // project for the same server. Not a cloud provider, so provider stays null.
  // The project and host must not end in `-`; checked after the match rather
  // than in the pattern, which would need overlapping repeats.
  const openshift = parseOpenShiftContext(name)
  if (openshift) {
    return {
      provider: null,
      account: null,
      region: null,
      clusterName: openshift,
      raw: name,
    }
  }

  // Other/unknown - just use the name as cluster name
  return {
    provider: null,
    account: null,
    region: null,
    clusterName: name,
    raw: name,
  }
}

const OPENSHIFT_CONTEXT = /^([a-z0-9][a-z0-9-]*)\/([a-z0-9][a-z0-9-]*|\[[0-9a-fA-F:.]+\]):(\d{1,5})\/(.+)$/

function parseOpenShiftContext(name: string): string | null {
  const m = name.match(OPENSHIFT_CONTEXT)
  if (!m) return null
  const [, project, host, port] = m
  const portNumber = Number(port)
  if (project.endsWith('-') || host.endsWith('-') || portNumber < 1 || portNumber > 65535) return null
  return host
}
