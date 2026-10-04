// The parts of a barman-cloud destination an on-call engineer checks when WAL
// archiving fails: where it writes and which Secrets hold its credentials.
// Secrets are named, never read.

export interface CNPGArchiveDestination {
  /** destinationPath, e.g. s3://bucket/prefix */
  path?: string
  endpointURL?: string
  /** Each Secret key the destination reads its credentials from. */
  secrets: { secret: string; key?: string; what: string }[]
  /** Credentials come from the workload's identity instead of a Secret. */
  identity?: string
  /** Where this configuration is declared. */
  declaredIn: string
}

const SECRET_FIELDS: [string, string, string][] = [
  ['s3Credentials', 'accessKeyId', 'S3 access key ID'],
  ['s3Credentials', 'secretAccessKey', 'S3 secret access key'],
  ['s3Credentials', 'region', 'S3 region'],
  ['s3Credentials', 'sessionToken', 'S3 session token'],
  ['azureCredentials', 'connectionString', 'Azure connection string'],
  ['azureCredentials', 'storageAccount', 'Azure storage account'],
  ['azureCredentials', 'storageKey', 'Azure storage key'],
  ['azureCredentials', 'storageSasToken', 'Azure SAS token'],
  ['googleCredentials', 'applicationCredentials', 'Google application credentials'],
]

/** Reads a barman-cloud object store configuration (in-tree `spec.backup.barmanObjectStore` or an ObjectStore's `spec.configuration`). */
export function cnpgArchiveDestination(config: any, declaredIn: string): CNPGArchiveDestination {
  const secrets: CNPGArchiveDestination['secrets'] = []
  for (const [group, field, what] of SECRET_FIELDS) {
    const ref = config?.[group]?.[field]
    if (typeof ref?.name === 'string' && ref.name) secrets.push({ secret: ref.name, key: typeof ref.key === 'string' ? ref.key : undefined, what })
  }
  const ca = config?.endpointCA
  if (typeof ca?.name === 'string' && ca.name) secrets.push({ secret: ca.name, key: typeof ca.key === 'string' ? ca.key : undefined, what: 'endpoint CA bundle' })
  const identity = config?.s3Credentials?.inheritFromIAMRole
    ? 'the Pod’s IAM role (inheritFromIAMRole)'
    : config?.azureCredentials?.inheritFromAzureAD
      ? 'the Pod’s Azure AD workload identity (inheritFromAzureAD)'
      : config?.googleCredentials?.gkeEnvironment
        ? 'the GKE workload identity (gkeEnvironment)'
        : undefined
  return {
    path: typeof config?.destinationPath === 'string' ? config.destinationPath : undefined,
    endpointURL: typeof config?.endpointURL === 'string' ? config.endpointURL : undefined,
    secrets,
    identity,
    declaredIn,
  }
}
