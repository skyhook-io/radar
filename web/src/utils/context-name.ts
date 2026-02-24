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
  // GKE format: gke_{project}_{region}_{cluster-name}
  const gkeMatch = name.match(/^gke_([^_]+)_([^_]+)_(.+)$/)
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

  // AKS format: try to detect
  if (name.toLowerCase().includes('aks') || name.includes('.azure.') || name.includes('azurecr')) {
    return {
      provider: 'AKS',
      account: null,
      region: null,
      clusterName: name,
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
