import yaml from 'yaml'
import { isApiGroup, parseQuantityToNumber } from '@skyhook-io/k8s-ui'
import { validateRFC1123Label } from '@skyhook-io/k8s-ui/utils/validators'

export interface CNPGTarget {
  name: string
  namespace: string
  instances: string
  size: string
  storageClass: string
  image: string
}

export function cnpgTargetFromManifest(manifest: Record<string, any>): CNPGTarget {
  const storage = manifest.spec?.storage
  return {
    name: manifest.metadata?.name ?? '',
    namespace: manifest.metadata?.namespace ?? '',
    instances: String(manifest.spec?.instances ?? 1),
    size: storage?.size ?? storage?.pvcTemplate?.resources?.requests?.storage ?? '',
    storageClass: storage?.storageClass ?? storage?.pvcTemplate?.storageClassName ?? '',
    image: manifest.spec?.imageName ?? '',
  }
}

export function cnpgTargetIssue(target: CNPGTarget, requireImage = false): string | undefined {
  const name = validateRFC1123Label(target.name.trim())
  if (!name.valid) return `Cluster name ${name.error}.`
  const namespace = validateRFC1123Label(target.namespace.trim())
  if (!namespace.valid) return `Namespace ${namespace.error}.`
  if (
    !/^\d+$/.test(target.instances) ||
    Number(target.instances) < 1 ||
    !Number.isSafeInteger(Number(target.instances))
  )
    return 'Choose a positive whole number of instances.'
  if (!(parseQuantityToNumber(target.size.trim()) > 0)) return 'Enter a positive data volume size, such as 20Gi.'
  if (requireImage && !target.image.trim()) return 'Enter the image matching the source PostgreSQL major version.'
}

export function withCNPGTarget(manifest: Record<string, any>, target: CNPGTarget): Record<string, any> {
  const out = structuredClone(manifest)
  out.metadata = { ...out.metadata, name: target.name.trim(), namespace: target.namespace.trim() }
  out.spec.instances = Number(target.instances)
  const storage = out.spec.storage ?? (out.spec.storage = {})
  if (storage.size !== undefined || !storage.pvcTemplate?.resources?.requests?.storage)
    storage.size = target.size.trim()
  else storage.pvcTemplate.resources.requests.storage = target.size.trim()
  if (target.storageClass.trim() !== cnpgTargetFromManifest(manifest).storageClass) {
    if (target.storageClass.trim()) storage.storageClass = target.storageClass.trim()
    else {
      delete storage.storageClass
      if (storage.pvcTemplate && storage.pvcTemplate.storageClassName !== '')
        delete storage.pvcTemplate.storageClassName
    }
  }
  if (!out.spec.imageCatalogRef) {
    if (target.image.trim()) out.spec.imageName = target.image.trim()
    else delete out.spec.imageName
  }
  return out
}

export function cnpgTargetDraft(text: string): Record<string, any> | null {
  try {
    const documents = yaml.parseAllDocuments(text, { version: '1.1' })
    if (documents.length !== 1 || documents[0].errors.length > 0) return null
    const document = documents[0]
    for (const path of [
      ['spec'],
      ['spec', 'storage'],
      ['spec', 'storage', 'pvcTemplate'],
      ['spec', 'storage', 'pvcTemplate', 'resources'],
      ['spec', 'storage', 'pvcTemplate', 'resources', 'requests'],
    ]) {
      const node = document.getIn(path, true)
      if (node !== undefined && !yaml.isMap(node)) return null
    }
    const manifest = documents[0].toJS()
    if (
      manifest?.kind !== 'Cluster' ||
      !isApiGroup(manifest.apiVersion, 'postgresql.cnpg.io') ||
      !manifest.spec ||
      typeof manifest.metadata?.name !== 'string' ||
      typeof manifest.metadata?.namespace !== 'string'
    )
      return null
    const target = cnpgTargetFromManifest(manifest)
    if ([target.size, target.storageClass, target.image].some((value) => typeof value !== 'string')) return null
    return manifest
  } catch {
    return null
  }
}

export function updateCNPGTargetYaml(text: string, target: CNPGTarget): string | null {
  const current = cnpgTargetDraft(text)
  if (!current) return null
  const next = withCNPGTarget(current, target)
  const document = yaml.parseDocument(text, { version: '1.1' })
  const paths = [
    ['metadata', 'name'],
    ['metadata', 'namespace'],
    ['spec', 'instances'],
    ['spec', 'storage', 'size'],
    ['spec', 'storage', 'storageClass'],
    ['spec', 'storage', 'pvcTemplate', 'resources', 'requests', 'storage'],
    ['spec', 'storage', 'pvcTemplate', 'storageClassName'],
    ['spec', 'imageName'],
  ]
  for (const path of paths) {
    const at = (object: Record<string, any>) => path.reduce<any>((value, key) => value?.[key], object)
    if (at(current) === at(next)) continue
    if (at(next) === undefined) document.deleteIn(path)
    else {
      // YAML 1.1 creates !!omap for missing setIn parents; Kubernetes expects plain mappings.
      for (let depth = 1; depth < path.length; depth++) {
        const parent = path.slice(0, depth)
        if (!document.hasIn(parent)) document.setIn(parent, document.createNode({}))
      }
      document.setIn(path, at(next))
    }
  }
  return document.toString()
}

export function sameCNPGRecoveryIdentity(left: Record<string, any>, right: Record<string, any>): boolean {
  const identity = (manifest: Record<string, any>) => ({
    bootstrap: manifest.spec.bootstrap,
    externalClusters: manifest.spec.externalClusters,
    imageName: manifest.spec.imageName,
    imageCatalogRef: manifest.spec.imageCatalogRef,
  })
  return (
    yaml.stringify(identity(left), { sortMapEntries: true }) ===
    yaml.stringify(identity(right), { sortMapEntries: true })
  )
}

export function onlyCNPGTargetChanged(
  before: Record<string, any>,
  after: Record<string, any>,
  target: CNPGTarget,
): boolean {
  return (
    yaml.stringify(withCNPGTarget(before, target), { sortMapEntries: true }) ===
    yaml.stringify(after, { sortMapEntries: true })
  )
}
