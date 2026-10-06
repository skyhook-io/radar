import { useCNPGKubectlContext } from './useCNPGKubectlContext'
import type { ReactNode } from 'react'
import {
  CNPG_GROUP, CNPG_BARMAN_OBJECTSTORE_GROUP, CNPGClusterCertificates, CNPGConnectSection,
  FactGrid, FactRow, FoldSection, KeyValueBadgeList, PaneLoader, RefLink, SectionHeading,
  cnpgConnectInfo, getCNPGClusterBackupConfig, getCNPGClusterPostgresParams,
  getCNPGClusterReplicaSource, refToSelectedResource, type NavigateToRef,
} from '@skyhook-io/k8s-ui'
import { useResource } from '../../api/client'
import type { SelectedResource } from '../../types'
import { buildWorkloadPath } from '../../utils/navigation'
import { RefreshFailedNotice } from '../workspace/layout'
import { useCNPGClusterAssessment } from './useCNPGClusterAssessment'
import { useCNPGNavigate } from './useCNPGNavigate'
import { CNPGParametersInEffect } from './CNPGParametersInEffect'
import { preflightFacts } from './recovery/restoreModel'
import { cnpgScreenPath } from './routes'
import { useConnection } from '../../context/ConnectionContext'
import { currentPageLabel } from '../../utils/page-links'

const cardClass = 'rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm'

export function CNPGConfiguration({ namespace, name, onNavigate, onSelectTab }: {
  namespace: string; name: string; onNavigate: (r: SelectedResource) => void; onSelectTab: (tab: string) => void
}) {
  const kubeconfigContext = useCNPGKubectlContext()
  const object = useResource<any>('clusters', namespace, name, CNPG_GROUP)
  const { query, row, ha } = useCNPGClusterAssessment(namespace, name)
  const navigate = useCNPGNavigate()
  const { connection } = useConnection()
  const go: NavigateToRef = (ref) => onNavigate(refToSelectedResource(ref))
  const cluster = object.data
  if (!cluster) return object.isLoading ? <PaneLoader label="Loading Configuration…" className="h-40" /> : (
    <p className="p-4 text-sm text-theme-text-tertiary">{object.error instanceof Error ? object.error.message : 'The Cluster could not be read.'}</p>
  )
  const info = cnpgConnectInfo(cluster, row?.poolerObjects)
  const services = info.endpoints.filter((e) => e.role !== 'pooler').map((e) => e.name).join(', ')
  const poolers = row?.poolersKnown ? info.endpoints.filter((e) => e.role === 'pooler').map((e) => e.name).join(', ') || 'none' : 'not read'
  return (
    <div className="space-y-4 p-4">
      <RefreshFailedNotice queries={[object, ha, query]} />
      <section className={cardClass}>
        <FoldSection title="Connect" summary={`Services ${services} · poolers ${poolers} · database ${info.database.value ?? 'unknown'}`} attention={false}>
          <CNPGConnectSection ha={ha.data} haUnavailableReason={ha.isLoading ? 'Reading availability…' : ha.error instanceof Error ? `Availability could not be read: ${ha.error.message}` : undefined} kubeconfigContext={kubeconfigContext?.name} kubeconfigSource={kubeconfigContext?.source} cluster={cluster} poolers={row?.poolerObjects} poolersKnown={row?.poolersKnown ?? false} showHeading={false} onNavigate={go}
            onOpenReachability={(svc) => navigate(buildWorkloadPath({ kind: 'services', group: '', namespace: svc.namespace, name: svc.name, tab: 'reachability' }))} />
        </FoldSection>
      </section>
      <section className={cardClass}>
        <CNPGParametersInEffect namespace={namespace} name={name} declared={getCNPGClusterPostgresParams(cluster)} scope={{ declaredInstances: cluster.spec?.instances, expectedInstances: [...new Set<string>([...(cluster.status?.instanceNames ?? []), ...(ha.data?.expectedInstances ?? [])])] }} />
      </section>
      <CNPGDeclaredSettings cluster={cluster} onNavigate={go} onSelectTab={onSelectTab} onOpenDeclarations={() => navigate(`${cnpgScreenPath('declarations')}?${new URLSearchParams({ cluster: `${namespace}/${name}` })}`, { state: { returnLabel: currentPageLabel(), returnCtx: connection.context } })} />
      <section className={cardClass}>
        {ha.data ? <CNPGClusterCertificates ha={ha.data} onNavigate={go} /> : <>
          <SectionHeading>Certificates</SectionHeading>
          <p className="text-sm text-theme-text-tertiary">{ha.isLoading ? 'Reading certificates…' : ha.error instanceof Error ? `Certificates could not be read: ${ha.error.message}` : 'Certificates could not be read.'}</p>
        </>}
      </section>
      <section className={cardClass}>
        <FoldSection title="Labels and annotations" summary={`${Object.keys(cluster.metadata?.labels ?? {}).length} labels · ${Object.keys(cluster.metadata?.annotations ?? {}).length} annotations`} attention={false}>
          <SectionHeading>Labels</SectionHeading>
          {Object.keys(cluster.metadata?.labels ?? {}).length > 0 ? <KeyValueBadgeList items={cluster.metadata.labels} /> : <p className="text-sm text-theme-text-tertiary">No labels</p>}
          <SectionHeading>Annotations</SectionHeading>
          <FactGrid>{Object.entries(cluster.metadata?.annotations ?? {}).map(([key, value]) => <FactRow key={key} label={key}><span className="break-all">{String(value)}</span></FactRow>)}</FactGrid>
          {Object.keys(cluster.metadata?.annotations ?? {}).length === 0 && <p className="text-sm text-theme-text-tertiary">No annotations</p>}
        </FoldSection>
      </section>
    </div>
  )
}

type DeclaredFact = { label: string; path: string; value: ReactNode; raw?: string }

export function CNPGDeclaredSettings({ cluster, onNavigate, onSelectTab, onOpenDeclarations }: {
  cluster: any; onNavigate?: NavigateToRef; onSelectTab: (tab: string) => void; onOpenDeclarations: () => void
}) {
  const spec = cluster.spec ?? {}
  const ns = cluster.metadata?.namespace ?? ''
  const copied = preflightFacts(cluster).filter((f) => f.copied)
  const basic: DeclaredFact[] = copied.filter((f) => f.path === 'spec.instances' || (f.label === 'Image' && (spec.imageName || spec.imageCatalogRef)))
  if (spec.imageCatalogRef) {
    const ref = spec.imageCatalogRef
    const fact = basic.find((f) => f.label === 'Image')!
    fact.value = <><RefLink refTo={{ kind: ref.kind ?? 'ImageCatalog', group: CNPG_GROUP, namespace: ref.kind === 'ClusterImageCatalog' ? '' : ns, name: ref.name }} onNavigate={onNavigate} mono /> · major {ref.major}</>
  }
  const add = (facts: DeclaredFact[], label: string, path: string, value: unknown) => {
    if (value !== undefined && value !== null && value !== '' && !(Array.isArray(value) && value.length === 0)) facts.push({ label, path, value: typeof value === 'boolean' ? (value ? 'Enabled' : 'Disabled') : String(value) })
  }
  if (cluster.status?.image && (spec.imageCatalogRef || (spec.imageName && cluster.status.image !== spec.imageName))) {
    const image = basic.find((f) => f.label === 'Image')
    if (image) image.value = <>{image.value}<div className="text-xs text-theme-text-secondary">Target image (status.image): {cluster.status.image}</div></>
  }
  if (spec.primaryUpdateStrategy) basic.push({ label: 'Primary update strategy', path: 'spec.primaryUpdateStrategy', raw: spec.primaryUpdateStrategy, value: spec.primaryUpdateStrategy === 'unsupervised' ? 'Updates the primary automatically (switchover or restart per update method)' : spec.primaryUpdateStrategy === 'supervised' ? 'Waits for a manual switchover' : spec.primaryUpdateStrategy })
  if (spec.primaryUpdateMethod) basic.push({ label: 'Primary update method', path: 'spec.primaryUpdateMethod', raw: spec.primaryUpdateMethod, value: spec.primaryUpdateMethod === 'restart' ? 'Updates the primary in place, interrupting its connections' : spec.primaryUpdateMethod === 'switchover' ? 'Promotes an updated standby before updating the old primary' : spec.primaryUpdateMethod })
  add(basic, 'Superuser access', 'spec.enableSuperuserAccess', spec.enableSuperuserAccess)
  add(basic, 'Minimum sync replicas', 'spec.minSyncReplicas', spec.minSyncReplicas)
  add(basic, 'Maximum sync replicas', 'spec.maxSyncReplicas', spec.maxSyncReplicas)
  if (spec.postgresql?.synchronous) {
    for (const key of ['method', 'number', 'dataDurability', 'maxStandbyNamesFromCluster']) add(basic, `Synchronous ${key}`, `spec.postgresql.synchronous.${key}`, spec.postgresql.synchronous[key])
  }

  const placement: DeclaredFact[] = Object.keys(spec.resources?.requests ?? {}).length > 0 || Object.keys(spec.resources?.limits ?? {}).length > 0 ? copied.filter((f) => f.path === 'spec.resources') : []
  const affinity = spec.affinity
  if (affinity && Object.keys(affinity).length > 0) {
    const bits: string[] = []
    if (affinity.enablePodAntiAffinity !== undefined) bits.push(`pod anti-affinity ${affinity.enablePodAntiAffinity ? 'enabled' : 'disabled'}`)
    if (affinity.podAntiAffinityType) placement.push({ label: 'Pod anti-affinity', path: 'spec.affinity.podAntiAffinityType', raw: affinity.podAntiAffinityType, value: affinity.enablePodAntiAffinity === false ? `Not applied: pod anti-affinity is disabled` : affinity.podAntiAffinityType === 'preferred' ? `Spreads instances across ${affinity.topologyKey && affinity.topologyKey !== 'kubernetes.io/hostname' ? 'topology domains' : 'nodes'} when possible` : affinity.podAntiAffinityType === 'required' ? `Requires instances on separate ${affinity.topologyKey && affinity.topologyKey !== 'kubernetes.io/hostname' ? 'topology domains' : 'nodes'}` : affinity.podAntiAffinityType })
    if (affinity.topologyKey) bits.push(`topology ${affinity.topologyKey}`)
    if (Object.keys(affinity.nodeSelector ?? {}).length > 0) bits.push(`nodes ${Object.entries(affinity.nodeSelector).map(([k, v]) => `${k}=${v}`).join(', ')}`)
    for (const key of ['nodeAffinity', 'podAffinity', 'podAntiAffinity', 'additionalPodAffinity', 'additionalPodAntiAffinity', 'tolerations']) {
      if (affinity[key] && Object.keys(affinity[key]).length > 0) bits.push(`${key} declared`)
    }
    add(placement, 'Affinity', 'spec.affinity', bits.join(' · '))
  }
  add(placement, 'Topology spread', 'spec.topologySpreadConstraints', spec.topologySpreadConstraints?.map((c: any) => `${c.topologyKey} · max skew ${c.maxSkew} · ${c.whenUnsatisfiable}`).join('; '))
  add(placement, 'Priority class', 'spec.priorityClassName', spec.priorityClassName)

  const storage: DeclaredFact[] = []
  for (const volume of [
    { label: 'Data', path: 'spec.storage', config: spec.storage },
    { label: 'WAL', path: 'spec.walStorage', config: spec.walStorage },
    ...(spec.tablespaces ?? []).map((t: any) => ({ label: `Tablespace ${t.name}`, path: `spec.tablespaces[name=${t.name}].storage`, config: t.storage })),
  ]) {
    if (!volume.config) continue
    if (volume.config.size !== undefined) add(storage, `${volume.label} size`, `${volume.path}.size`, volume.config.size)
    else add(storage, `${volume.label} size`, `${volume.path}.pvcTemplate.resources.requests.storage`, volume.config.pvcTemplate?.resources?.requests?.storage)
    if (volume.config.storageClass !== undefined) add(storage, `${volume.label} storage class`, `${volume.path}.storageClass`, volume.config.storageClass)
    else add(storage, `${volume.label} storage class`, `${volume.path}.pvcTemplate.storageClassName`, volume.config.pvcTemplate?.storageClassName)
  }
  const bootstrap: DeclaredFact[] = []
  add(bootstrap, 'Application database', 'spec.bootstrap.initdb.database', spec.bootstrap?.initdb?.database)
  add(bootstrap, 'Owner', 'spec.bootstrap.initdb.owner', spec.bootstrap?.initdb?.owner)
  if (spec.bootstrap?.initdb && bootstrap.length === 0) add(bootstrap, 'Bootstrap', 'spec.bootstrap.initdb', 'initdb declared')
  add(bootstrap, 'Recovery source', 'spec.bootstrap.recovery.source', spec.bootstrap?.recovery?.source)
  if (spec.bootstrap?.recovery?.backup?.name) bootstrap.push({ label: 'Recovery backup', path: 'spec.bootstrap.recovery.backup.name', value: <RefLink refTo={{ kind: 'Backup', group: CNPG_GROUP, namespace: ns, name: spec.bootstrap.recovery.backup.name }} onNavigate={onNavigate} mono /> })
  if (spec.bootstrap?.recovery && !spec.bootstrap.recovery.source && !spec.bootstrap.recovery.backup?.name) add(bootstrap, 'Bootstrap', 'spec.bootstrap.recovery', 'recovery declared')
  add(bootstrap, 'Base backup source', 'spec.bootstrap.pg_basebackup.source', spec.bootstrap?.pg_basebackup?.source)
  if (spec.replica) {
    if (spec.replica.source || spec.replica.primary) add(bootstrap, 'Replica source', `spec.replica.${spec.replica.source ? 'source' : 'primary'}`, getCNPGClusterReplicaSource(cluster))
    add(bootstrap, 'Replica enabled', 'spec.replica.enabled', spec.replica.enabled)
  }
  add(bootstrap, 'External clusters', 'spec.externalClusters', spec.externalClusters?.map((c: any) => c.name).join(', '))

  const backups: DeclaredFact[] = []
  const backup = getCNPGClusterBackupConfig(cluster)
  if (spec.backup?.barmanObjectStore) {
    add(backups, 'Method', 'spec.backup.barmanObjectStore', 'barmanObjectStore')
    add(backups, 'Destination', 'spec.backup.barmanObjectStore.destinationPath', backup.destinationPath)
  }
  if (backup.plugin) {
    add(backups, 'Method', 'spec.plugins', `plugin ${backup.plugin.name}`)
    const pluginPath = `spec.plugins[name=${backup.plugin.name}]`
    const declaredPlugin = spec.plugins.find((p: any) => p.name === backup.plugin!.name)
    add(backups, 'WAL archiver', `${pluginPath}.isWALArchiver`, declaredPlugin.isWALArchiver)
    add(backups, 'Archive server', `${pluginPath}.parameters.serverName`, declaredPlugin.parameters?.serverName)
    if (backup.plugin.barmanObjectName) backups.push({ label: 'Destination', path: `spec.plugins[name=${backup.plugin.name}].parameters.barmanObjectName`, value: <RefLink refTo={{ kind: 'ObjectStore', group: CNPG_BARMAN_OBJECTSTORE_GROUP, namespace: ns, name: backup.plugin.barmanObjectName }} onNavigate={onNavigate} mono /> })
  }
  if (spec.backup?.volumeSnapshot) {
    add(backups, 'Method', 'spec.backup.volumeSnapshot', 'volumeSnapshot')
    add(backups, 'Snapshot class', 'spec.backup.volumeSnapshot.className', spec.backup.volumeSnapshot.className)
    add(backups, 'WAL snapshot class', 'spec.backup.volumeSnapshot.walClassName', spec.backup.volumeSnapshot.walClassName)
    add(backups, 'Snapshot ownership', 'spec.backup.volumeSnapshot.snapshotOwnerReference', spec.backup.volumeSnapshot.snapshotOwnerReference)
  }
  add(backups, 'Retention', 'spec.backup.retentionPolicy', backup.retentionPolicy)
  add(backups, 'Backup target', 'spec.backup.target', spec.backup?.target)

  const other: DeclaredFact[] = []
  if (spec.managed?.roles?.length > 0) other.push({ label: 'Managed roles', path: 'spec.managed.roles', value: <>{spec.managed.roles.length} · <button type="button" onClick={onOpenDeclarations} className="text-xs text-accent-text hover:underline">Declarations →</button></> })
  add(other, 'Additional services', 'spec.managed.services.additional', spec.managed?.services?.additional?.map((s: any) => s.serviceTemplate?.metadata?.name).join(', '))
  add(other, 'Disabled services', 'spec.managed.services.disabledDefaultServices', spec.managed?.services?.disabledDefaultServices?.join(', '))
  add(other, 'PodMonitor', 'spec.monitoring.enablePodMonitor', spec.monitoring?.enablePodMonitor)
  for (const [field, kind] of [['customQueriesConfigMap', 'ConfigMap'], ['customQueriesSecret', 'Secret']] as const) {
    if (spec.monitoring?.[field]?.length > 0) other.push({ label: `Query ${kind}s`, path: `spec.monitoring.${field}`, value: <div className="flex flex-wrap gap-x-3">{spec.monitoring[field].map((ref: { name: string; key: string }) => <span key={`${ref.name}/${ref.key}`}><RefLink refTo={{ kind, group: '', namespace: ns, name: ref.name }} onNavigate={onNavigate} mono /> · key {ref.key}</span>)}</div> })
  }
  add(other, 'Plugins', 'spec.plugins', spec.plugins?.map((p: any) => `${p.name}${p.enabled === false ? ' (disabled)' : ''}`).join(', '))

  return <>{[
    { title: 'Instances and image', facts: basic },
    { title: 'Placement and resources', facts: placement },
    { title: 'Storage', facts: storage, link: 'Usage → Storage tab', tab: 'storage' },
    { title: 'Bootstrap and source', facts: bootstrap },
    { title: 'Backups configuration', facts: backups, link: 'Runs and recovery evidence → Backups tab', tab: 'backups' },
    { title: 'Roles, services and monitoring', facts: other },
  ].filter((c) => c.facts.length > 0).map((c) => <section key={c.title} className={cardClass}>
    <SectionHeading>{c.title}</SectionHeading>
    <FactGrid>{c.facts.map((f) => <FactRow key={`${f.label}/${f.path}`} label={f.label}>
      {f.value}<div className="mt-0.5 text-[11px] text-theme-text-tertiary">{f.raw ? `${f.raw} · ${f.path}` : f.path}</div>
    </FactRow>)}</FactGrid>
    {c.tab && <button type="button" onClick={() => onSelectTab(c.tab!)} className="mt-2 text-xs text-accent-text hover:underline">{c.link}</button>}
  </section>)}</>
}
