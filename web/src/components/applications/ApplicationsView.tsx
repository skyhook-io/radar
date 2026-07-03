import { useCallback, useEffect, useMemo } from 'react'
import { useSearchParams } from 'react-router-dom'
import {
  ApplicationsList,
  ApplicationDetail,
  CenteredEmpty,
  PageHeader,
  FreshnessControl,
  useToast,
  orderEnvs,
  matchWorkloadAcrossInstances,
  workloadKey,
  healthOf,
  compareVersions,
  type AppRow,
  type AppIdentityInstance,
  type SelectedAppWorkload,
  type SelectedResource,
} from '@skyhook-io/k8s-ui'
import { Boxes } from 'lucide-react'
import { useApplications, useTopology } from '../../api/client'
import { useConnection } from '../../context/ConnectionContext'
import { kindToPlural } from '../../utils/navigation'
import { WorkloadView } from '../workload/WorkloadView'

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
  // closing an app also clears the per-app params (workload, tab).
  const [searchParams, setSearchParams] = useSearchParams()
  const selectedKey = searchParams.get('app')
  const selected = useMemo(() => apps.find((a) => a.key === selectedKey) ?? null, [apps, selectedKey])

  const selectApp = useCallback(
    (key: string | null) => {
      const params = new URLSearchParams(searchParams)
      if (key) params.set('app', key)
      else params.delete('app')
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
// the resources-view topology over the app's namespaces (for the app graph)
// and the per-workload WorkloadView (which fetches its own topology for the
// Topology tab). Split out so useTopology runs unconditionally (Rules of Hooks).
function AppDetailRoute({ app, apps, onBack, onOpenResource }: { app: AppRow; apps: AppRow[]; onBack: () => void; onOpenResource: (resource: SelectedResource) => void }) {
  const appNamespaces = useMemo(
    () => Array.from(new Set((app.workloads ?? []).map((w) => w.namespace).filter(Boolean))).sort(),
    [app.workloads],
  )
  const { data: topology, isLoading: topologyLoading } = useTopology(appNamespaces, 'resources', { enabled: appNamespaces.length > 0 })

  // The selected workload (?workload=<key>) lives in the URL too: deep-linkable,
  // and back returns from a workload's runtime to the app graph. Clearing it
  // also drops the workload's tab param.
  const [searchParams, setSearchParams] = useSearchParams()
  const selectedWorkloadKey = searchParams.get('workload')
  const selectWorkload = useCallback(
    (key: string | null) => {
      const params = new URLSearchParams(searchParams)
      // Always drop the workload's tab: a fresh workload opens on its overview,
      // and clearing back to the graph leaves no stale tab on the route.
      params.delete('tab')
      if (key) params.set('workload', key)
      else params.delete('workload')
      setSearchParams(params)
    },
    [searchParams, setSearchParams],
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
        // instance's overview and say so. (With no workload selected the tab
        // rides along: it applies to the lone workload either side.)
        params.delete('workload');
        params.delete('tab');
        if (target) {
          showToast(`No matching workload in ${target.identity?.env ?? target.name}`, { detail: 'Showing the instance overview instead.', type: 'info' });
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
    <div className="flex-1 overflow-auto">
      <ApplicationDetail
        app={app}
        onBack={onBack}
        topology={topology}
        topologyLoading={topologyLoading}
        identityInstances={identityInstances}
        onSwitchInstance={switchInstance}
        discoveredEnvs={discoveredEnvs}
        onNavigateToResource={onOpenResource}
        selectedWorkloadKey={selectedWorkloadKey}
        onSelectWorkload={selectWorkload}
        renderWorkload={(workload: SelectedAppWorkload) => (
          <div className="h-full overflow-hidden">
            <WorkloadView
              kind={kindToPlural(workload.kind)}
              namespace={workload.namespace}
              name={workload.name}
              onBack={() => selectWorkload(null)}
              // "Back" returns to the app graph — meaningless for a
              // single-workload app, which has no graph to return to.
              hideBackButton={(app.workloads?.length ?? 0) <= 1}
              compactHeader
              onNavigateToResource={onOpenResource}
            />
          </div>
        )}
      />
    </div>
  )
}
