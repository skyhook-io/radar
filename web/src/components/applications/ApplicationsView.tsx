import { useCallback, useEffect, useMemo, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import {
  ApplicationsList,
  ApplicationDetail,
  CenteredEmpty,
  PageHeader,
  FreshnessControl,
  IssueRow,
  ISSUE_SEVERITY_RANK,
  useToast,
  orderEnvs,
  matchWorkloadAcrossInstances,
  workloadKey,
  healthOf,
  compareVersions,
  gitOpsRouteForKind,
  deploymentInventoryFromGitOps,
  deploymentInventoryFromHelm,
  memberRef,
  subjectRef,
  type AppRow,
  type AppWorkload,
  type AppIdentityInstance,
  type ApplicationView,
  type AppSourceRef,
  type Issue,
  type IssueResourceRef,
  type SelectedAppWorkload,
  type SelectedResource,
} from '@skyhook-io/k8s-ui'
import { AlertTriangle, Boxes } from 'lucide-react'
import { SEVERITY_TEXT } from '@skyhook-io/k8s-ui/utils/badge-colors'
import { useApplicationHistory, useApplications, useGitOpsTree, useHelmRelease, useIssues, useTopology, type IssuesResponse } from '../../api/client'
import { useConnection } from '../../context/ConnectionContext'
import { buildWorkloadPath, kindToPlural } from '../../utils/navigation'
import { WorkloadView } from '../workload/WorkloadView'

const APPLICATION_VIEWS: ReadonlySet<ApplicationView> = new Set<ApplicationView>(['overview', 'topology', 'history'])

function parseApplicationView(value: string | null): ApplicationView {
  if (!value || !APPLICATION_VIEWS.has(value as ApplicationView)) return 'overview'
  return value as ApplicationView
}

interface ApplicationsViewProps {
  namespaces: string[]
  onOpenResource: (resource: SelectedResource) => void
}

export function ApplicationsView({ namespaces, onOpenResource }: ApplicationsViewProps) {
  const query = useApplications(namespaces)
  const { connection } = useConnection()
  const apps = useMemo(() => query.data?.applications ?? [], [query.data])

  const freshness = (
    <FreshnessControl
      mode="auto"
      dataUpdatedAt={query.dataUpdatedAt}
      onRefresh={() => query.refetch()}
      connectionState={connection.state}
    />
  )

  // Which app is open lives in the URL (?app=<key>) so the detail view is
  // deep-linkable and the browser back button returns to the list. Opening or
  // closing an app also clears the per-app params (view, workload, tab).
  const [searchParams, setSearchParams] = useSearchParams()
  const selectedKey = searchParams.get('app')
  const selected = useMemo(() => apps.find((a) => a.key === selectedKey) ?? null, [apps, selectedKey])

  const selectApp = useCallback(
    (key: string | null) => {
      const params = new URLSearchParams(searchParams)
      if (key) params.set('app', key)
      else params.delete('app')
      params.delete('view')
      params.delete('workload')
      params.delete('tab')
      setSearchParams(params)
    },
    [searchParams, setSearchParams],
  )

  // A stale ?app= (uninstalled/renamed app, or a link from another cluster)
  // would leave the URL lying under the list view — clear it once data is
  // fresh. Never during load, so a slow fetch can't eject a valid deep link.
  useEffect(() => {
    if (selectedKey && !selected && query.isSuccess) {
      const params = new URLSearchParams(searchParams)
      params.delete('app')
      params.delete('view')
      params.delete('workload')
      params.delete('tab')
      setSearchParams(params, { replace: true })
    }
  }, [selectedKey, selected, query.isSuccess, searchParams, setSearchParams])

  if (selectedKey && selected) {
    return <AppDetailRoute app={selected} apps={apps} onBack={() => selectApp(null)} onOpenResource={onOpenResource} />
  }

  // The header + status + filters + table chassis lives inside ApplicationsList
  // (mirroring GitOpsTableView), which renders only on the data path. To keep
  // the page header from vanishing while loading / on error, the wrapper shows
  // the same header bar above those states. (Keep title + description in sync
  // with ApplicationsList's PageHeader.)
  if (query.isLoading) {
    return (
      <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
        <ApplicationsList apps={[]} onSelect={selectApp} headerActions={freshness} loading />
      </div>
    )
  }
  if (query.error) {
    return (
      <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
        <div className="shrink-0 border-b border-theme-border px-4 py-4">
          <PageHeader
            icon={Boxes}
            title="Applications"
            description="Deployable software in this cluster — your services, workers, and jobs, grouped by app/release evidence."
          />
        </div>
        <CenteredEmpty tone="filtered" icon={Boxes} headline="Failed to load applications" body={(query.error as Error).message} />
      </div>
    )
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
      <ApplicationsList apps={apps} onSelect={selectApp} headerActions={freshness} />
    </div>
  )
}

// AppDetailRoute wires the OSS data hooks the shared ApplicationDetail can't:
// the resources-view topology over the app's namespaces and the per-workload
// WorkloadView. Split out so useTopology runs unconditionally (Rules of Hooks).
function AppDetailRoute({ app, apps, onBack, onOpenResource }: { app: AppRow; apps: AppRow[]; onBack: () => void; onOpenResource: (resource: SelectedResource) => void }) {
  const navigate = useNavigate()
  const appNamespaces = useMemo(
    () => Array.from(new Set((app.workloads ?? []).map((w) => w.namespace).filter(Boolean))).sort(),
    [app.workloads],
  )
  const appHistoryNamespaces = useMemo(() => {
    const namespaces = new Set(appNamespaces)
    if (app.sourceRef?.namespace) namespaces.add(app.sourceRef.namespace)
    return Array.from(namespaces).sort()
  }, [app.sourceRef?.namespace, appNamespaces])
  const { data: topology, isLoading: topologyLoading } = useTopology(appNamespaces, 'resources', {
    enabled: appNamespaces.length > 0,
    includeReplicaSets: true,
    refetchInterval: 10_000,
  })
  const issuesQuery = useIssues(appNamespaces)
  const appIssues = useMemo(
    () => appIssuesForWorkloads(issuesQuery.data?.issues ?? [], app.workloads ?? []),
    [issuesQuery.data?.issues, app.workloads],
  )

  // The selected workload (?workload=<key>) is the scope switch and wins over
  // ?view= when both are present. With neither param, use the product default:
  // multi-workload apps open on app overview, single-workload apps open on the
  // workload. A single-workload app does not expose app scope.
  const [searchParams, setSearchParams] = useSearchParams()
  const viewParam = searchParams.get('view')
  const selectedView = parseApplicationView(viewParam)
  const selectedWorkloadParam = searchParams.get('workload')
  const appWorkloads = app.workloads ?? []
  const singleWorkloadKey = appWorkloads.length === 1 ? workloadKey(appWorkloads[0]) : null
  const selectedWorkloadKey = singleWorkloadKey ?? selectedWorkloadParam
  const historyQuery = useApplicationHistory(app.key, appHistoryNamespaces, { enabled: !selectedWorkloadKey })
  const sourceInventoryEnabled = !selectedWorkloadKey && selectedView === 'topology'
  const gitOpsSource = app.sourceRef?.type === 'gitops' ? app.sourceRef : undefined
  const deploymentTreeQuery = useGitOpsTree(
    gitOpsSource?.kind ?? '',
    gitOpsSource?.namespace ?? '',
    gitOpsSource?.name ?? '',
    gitOpsSource?.group,
    appHistoryNamespaces,
    { enabled: sourceInventoryEnabled },
  )
  const helmSource = app.sourceRef?.type === 'helm' ? app.sourceRef : undefined
  const helmReleaseQuery = useHelmRelease(helmSource?.namespace ?? '', helmSource?.name ?? '', { enabled: sourceInventoryEnabled })
  const deploymentInventory = useMemo(
    () => deploymentInventoryFromGitOps(deploymentTreeQuery.data) ?? deploymentInventoryFromHelm(helmReleaseQuery.data?.resources),
    [deploymentTreeQuery.data, helmReleaseQuery.data?.resources],
  )
  useEffect(() => {
    if (!singleWorkloadKey) return
    if (selectedWorkloadParam === singleWorkloadKey && !viewParam) return
    const params = new URLSearchParams(searchParams)
    params.delete('view')
    params.delete('run')
    params.set('workload', singleWorkloadKey)
    setSearchParams(params, { replace: true })
  }, [searchParams, selectedWorkloadParam, setSearchParams, singleWorkloadKey, viewParam])
  const selectView = useCallback(
    (view: ApplicationView) => {
      const params = new URLSearchParams(searchParams)
      params.delete('tab')
      params.delete('run')
      if (singleWorkloadKey) {
        params.delete('view')
        params.set('workload', singleWorkloadKey)
      } else if (view === 'overview') {
        params.delete('view')
        params.delete('workload')
      } else {
        params.set('view', view)
        params.delete('workload')
      }
      setSearchParams(params)
    },
    [searchParams, setSearchParams, singleWorkloadKey],
  )
  const selectWorkload = useCallback(
    (key: string | null) => {
      const params = new URLSearchParams(searchParams)
      params.delete('run')
      if (key) {
        const wasInWorkloadScope = !!selectedWorkloadKey
        params.delete('view')
        params.set('workload', key)
        if (!wasInWorkloadScope) params.delete('tab')
      } else if (singleWorkloadKey) {
        params.delete('view')
        params.set('workload', singleWorkloadKey)
      } else {
        params.delete('workload')
        params.delete('tab')
        params.delete('view')
      }
      setSearchParams(params)
    },
    [searchParams, selectedWorkloadKey, setSearchParams, singleWorkloadKey],
  )
  const selectWorkloadRun = useCallback(
    (workload: SelectedAppWorkload, run: { kind: string; name: string; data: Record<string, unknown> }) => {
      const params = new URLSearchParams(searchParams)
      const runNamespace = typeof run.data?.namespace === 'string' ? run.data.namespace : workload.namespace
      params.delete('view')
      params.delete('tab')
      params.set('workload', workloadKey(workload))
      params.set('run', `${kindToPlural(run.kind)}/${runNamespace}/${run.name}`)
      setSearchParams(params)
    },
    [searchParams, setSearchParams],
  )
  const openWorkloadResource = useCallback(
    (resource: SelectedResource) => {
      if (kindToPlural(resource.kind).toLowerCase() !== 'pods') {
        onOpenResource(resource)
        return
      }

      const [pathname, rawSearch = ''] = buildWorkloadPath({ ...resource, kind: kindToPlural(resource.kind) }).split('?')
      const params = new URLSearchParams(rawSearch)
      const activeNamespaces = searchParams.get('namespaces')
      if (activeNamespaces) params.set('namespaces', activeNamespaces)
      navigate({ pathname, search: params.toString() })
    },
    [navigate, onOpenResource, searchParams],
  )
  const openSource = useCallback(
    (source: AppSourceRef) => {
      if (source.type === 'gitops') {
        const path = gitOpsRouteForKind(source.kind, source.namespace, source.name)
        if (path) navigate(path)
        return
      }
      if (source.type === 'helm') {
        const params = new URLSearchParams()
        const activeNamespaces = searchParams.get('namespaces')
        if (activeNamespaces) params.set('namespaces', activeNamespaces)
        params.set('release', `${source.namespace}/${source.name}`)
        navigate({ pathname: '/helm', search: params.toString() })
      }
    },
    [navigate, searchParams],
  )

  // App identity switcher data: this instance's siblings (ladder-ordered
  // digests). It switches between REAL instances — ?app= changes, deep links
  // stay instance-keyed.
  const { showToast } = useToast();
  const identityInstances = useMemo<AppIdentityInstance[] | null>(() => {
    const fam = app.identity;
    if (!fam) return null;
    const sibs = apps.filter((a) => a.identity?.key === fam.key);
    if (sibs.length < 2) return null;
    const newest = (a: AppRow) =>
      (a.versions ?? []).reduce<string | undefined>((best, v) => (!best || compareVersions(v, best) === 1 ? v : best), undefined) ?? a.appVersion;
    const order = orderEnvs(sibs.map((a) => a.identity!.env));
    return [...sibs]
      .sort((a, b) => order.indexOf(a.identity!.env) - order.indexOf(b.identity!.env) || a.name.localeCompare(b.name))
      .map((a) => ({
        appKey: a.key,
        name: a.name,
        env: a.identity!.env,
        health: healthOf(a.health),
        version: newest(a),
        confidence: a.identity!.confidence,
        evidence: a.identity!.evidence,
      }));
  }, [apps, app]);

  // Position-preserving env switch: carry the selected workload + tab into the
  // sibling when a matching workload exists there (exact kind+name, else the
  // env-affix-stripped stem); otherwise land on the instance overview and say
  // the workload wasn't found.
  const switchInstance = useCallback(
    (targetKey: string) => {
      const target = apps.find((a) => a.key === targetKey);
      const params = new URLSearchParams(searchParams);
      params.set('app', targetKey);
      params.delete('run');
      const wk = params.get('workload');
      let matched = false;
      if (wk && target) {
        // Stem matching strips this app group's own env tokens too, so
        // discovered envs (loadtest, …) carry position like the trio does.
        const identityEnvs = new Set((identityInstances ?? []).map((i) => i.env));
        const m = matchWorkloadAcrossInstances(wk, target.workloads, identityEnvs);
        if (m) {
          params.set('workload', workloadKey(m));
          matched = true;
        }
      }
      if (!matched && wk) {
        // A workload WAS selected but has no counterpart — land on the target
        // instance's default scope and say so. Single-workload instances default
        // to their workload; composed apps default to app overview.
        params.delete('workload');
        params.delete('tab');
        const soleTargetWorkload = target?.workloads?.length === 1 ? target.workloads[0] : null;
        if (soleTargetWorkload) {
          params.delete('view');
          params.set('workload', workloadKey(soleTargetWorkload));
        } else {
          params.delete('view');
        }
        if (target) {
          showToast(`No matching workload in ${target.identity?.env ?? target.name}`, { detail: soleTargetWorkload ? 'Showing the instance workload instead.' : 'Showing the instance overview instead.', type: 'info' });
        }
      }
      setSearchParams(params);
    },
    [apps, identityInstances, searchParams, setSearchParams, showToast],
  );

  const discoveredEnvs = useMemo(
    () => new Set(apps.map((a) => a.identity?.env).filter((e): e is string => !!e)),
    [apps],
  );

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
      <ApplicationDetail
        app={app}
        onBack={onBack}
        topology={topology}
        topologyLoading={topologyLoading}
        deploymentInventory={deploymentInventory}
        identityInstances={identityInstances}
        onSwitchInstance={switchInstance}
        discoveredEnvs={discoveredEnvs}
        onNavigateToResource={onOpenResource}
        onSelectWorkloadRun={selectWorkloadRun}
        history={historyQuery.data}
        historyLoading={historyQuery.isLoading}
        onOpenSource={openSource}
        selectedWorkloadKey={selectedWorkloadKey}
        onSelectWorkload={selectWorkload}
        selectedView={selectedView}
        onSelectView={selectView}
        renderOverviewIssues={() => (
          <AppOverviewIssues
            issues={appIssues}
            error={issuesQuery.error}
            hasData={Boolean(issuesQuery.data)}
            visibility={issuesQuery.data?.visibility}
            onOpenResource={onOpenResource}
          />
        )}
        hasOverviewIssues={appIssues.length > 0}
        renderWorkload={(workload: SelectedAppWorkload) => (
          <div className="h-full overflow-hidden">
            <WorkloadView
              kind={kindToPlural(workload.kind)}
              group={workload.group}
              namespace={workload.namespace}
              name={workload.name}
              onBack={() => selectWorkload(null)}
              hideBackButton
              compactHeader
              pushTabHistory
              onNavigateToResource={openWorkloadResource}
            />
          </div>
        )}
      />
    </div>
  )
}

function AppOverviewIssues({ issues, error, hasData, visibility, onOpenResource }: { issues: Issue[]; error: unknown; hasData: boolean; visibility?: IssuesResponse['visibility']; onOpenResource: (resource: SelectedResource) => void }) {
  const [openId, setOpenId] = useState<string | null>(null)
  const sorted = useMemo(() => [...issues].sort(compareAppOverviewIssues), [issues])

  if (error && !hasData) {
    return <AppOverviewIssuesState headline="Operational issues unavailable" body={error instanceof Error ? error.message : 'Radar could not load issues for this application.'} />
  }

  if (error) {
    return (
      <section className="space-y-2">
        <AppOverviewIssuesState headline="Operational issue refresh failed" body="Showing the last successful result for this application." />
        {sorted.length > 0 && <AppOverviewIssueRows issues={sorted} openId={openId} setOpenId={setOpenId} onOpenResource={onOpenResource} />}
      </section>
    )
  }

  if (visibility?.impact) {
    return (
      <section className="space-y-2">
        <AppOverviewIssuesState headline={sorted.length === 0 ? 'No visible operational issues' : 'Operational issue visibility is limited'} body={`${visibility.impact} Results may be incomplete.`} />
        {sorted.length > 0 && <AppOverviewIssueRows issues={sorted} openId={openId} setOpenId={setOpenId} onOpenResource={onOpenResource} />}
      </section>
    )
  }

  if (sorted.length === 0) return null

  return <AppOverviewIssueRows issues={sorted} openId={openId} setOpenId={setOpenId} onOpenResource={onOpenResource} />
}

function AppOverviewIssuesState({ headline, body }: { headline: string; body: string }) {
  return (
    <section className="rounded-lg border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm">
      <div className="flex items-start gap-3">
        <AlertTriangle className={`mt-0.5 h-4 w-4 shrink-0 ${SEVERITY_TEXT.warning}`} aria-hidden />
        <div className="min-w-0">
          <h2 className="text-sm font-semibold text-theme-text-primary">{headline}</h2>
          <p className="mt-0.5 text-sm text-theme-text-secondary">{body}</p>
        </div>
      </div>
    </section>
  )
}

function AppOverviewIssueRows({ issues: sorted, openId, setOpenId, onOpenResource }: { issues: Issue[]; openId: string | null; setOpenId: (value: string | null) => void; onOpenResource: (resource: SelectedResource) => void }) {

  const navigate = (ref: IssueResourceRef) => {
    onOpenResource({
      kind: kindToPlural(ref.kind),
      namespace: ref.namespace ?? '',
      name: ref.name,
      group: ref.group ?? '',
    })
  }

  return (
    <section className="space-y-2">
      <div className="flex items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-2 text-sm font-semibold text-theme-text-primary">
          <AlertTriangle className="h-4 w-4 shrink-0 text-theme-text-secondary" aria-hidden />
          <span>Operational Issues</span>
          <span className="badge-sm text-[10px] text-theme-text-secondary">{sorted.length}</span>
        </div>
        <span className="text-xs text-theme-text-tertiary">Scoped to this application</span>
      </div>
      <ol className="flex flex-col gap-1.5">
        {sorted.slice(0, 4).map((issue) => {
          const rowKey = `${issue.cluster_id ?? ''}:${issue.id}`
          return (
            <IssueRow
              key={rowKey}
              issue={issue}
              open={openId === rowKey}
              onToggle={() => setOpenId(openId === rowKey ? null : rowKey)}
              onResourceClick={navigate}
            />
          )
        })}
      </ol>
      {sorted.length > 4 ? (
        <div className="text-xs text-theme-text-tertiary">
          Showing 4 of {sorted.length} issues. Open Issues for the full queue.
        </div>
      ) : null}
    </section>
  )
}

function compareAppOverviewIssues(a: Issue, b: Issue): number {
  const severity = ISSUE_SEVERITY_RANK[b.severity] - ISSUE_SEVERITY_RANK[a.severity]
  if (severity !== 0) return severity
  const fa = a.first_seen ?? ''
  const fb = b.first_seen ?? ''
  if (fa !== fb) return fb.localeCompare(fa)
  const ns = (a.namespace ?? '').localeCompare(b.namespace ?? '')
  if (ns !== 0) return ns
  const name = a.name.localeCompare(b.name)
  if (name !== 0) return name
  return a.id.localeCompare(b.id)
}

function appIssuesForWorkloads(issues: Issue[], workloads: AppWorkload[]): Issue[] {
  if (issues.length === 0 || workloads.length === 0) return []
  const workloadKeys = new Set(workloads.map(workloadIssueKey))
  const out: Issue[] = []
  const seen = new Set<string>()
  for (const issue of issues) {
    if (!issueRefs(issue).some((ref) => workloadKeys.has(issueRefKey(ref)))) continue
    if (seen.has(issue.id)) continue
    seen.add(issue.id)
    out.push(issue)
  }
  return out
}

function workloadIssueKey(workload: AppWorkload): string {
  return `${workload.kind.toLowerCase()}|${workload.namespace}|${workload.name}`
}

function issueRefKey(ref: IssueResourceRef): string {
  return `${ref.kind.toLowerCase()}|${ref.namespace ?? ''}|${ref.name}`
}

function issueRefs(issue: Issue): IssueResourceRef[] {
  const refs: IssueResourceRef[] = [subjectRef(issue)]
  if (issue.owner) refs.push(issue.owner)
  if (issue.incident_parent?.ref) refs.push(issue.incident_parent.ref)
  for (const member of issue.members ?? []) refs.push(memberRef(issue, member))
  for (const fact of issue.diagnostic_context?.facts ?? []) {
    refs.push(...(fact.refs ?? []))
    for (const related of fact.related_issues ?? []) refs.push(related.ref)
  }
  return refs
}
