import type { ArgoSyncDetailsProps } from "@skyhook-io/k8s-ui";
import { useArgoStatus, useArgoSyncDiff, useGitOpsInsights } from "../../api/client";
import { RevisionMetaChip } from "../gitops/RevisionMetaChip";
import { RenderedChanges } from "./RenderedChanges";

function SyncComparison({ namespace, name, syncID, previous, revision }: {
  namespace: string; name: string; syncID: string; previous: string; revision: string;
}) {
  const query = useArgoSyncDiff(namespace, name, syncID, previous, revision);
  if (query.isError) return <p role="status">Rendered changes unavailable. {query.error.message}</p>;
  if (query.isFetching || !query.data) return <p role="status">Comparing rendered resources…</p>;
  return <RenderedChanges diff={query.data} />;
}

export function ArgoSyncDetails({ source, revision, git }: ArgoSyncDetailsProps) {
  const status = useArgoStatus();
  const connected = !status.isError && status.data?.connected;
  const insights = useGitOpsInsights("applications", source.namespace, connected ? source.name : "", source.group);
  const commitAvailable = connected && !insights.isError && insights.data?.capabilities?.revisionMetadataAvailable &&
    insights.data.summary?.source?.split(" · ")[0] === git.repoURL;
  return <>
    {git.historyID && git.previousRevision && git.previousRevision !== revision && (
      connected
        ? <SyncComparison namespace={source.namespace} name={source.name} syncID={git.historyID} previous={git.previousRevision} revision={revision} />
        : <p className="text-xs" role="status">{status.isPending ? "Checking Argo CD connection…" :
          status.isError ? "Rendered changes unavailable. Radar could not check the Argo CD connection." :
          <>To compare rendered resources, <button type="button" className="font-medium text-accent-text hover:underline"
            onClick={() => window.dispatchEvent(new CustomEvent("radar:open-settings", { detail: { section: "argocd" } }))}>connect the Argo CD API in Settings</button>.</>}</p>
    )}
    {commitAvailable && <div className="flex flex-wrap items-center gap-2"><RevisionMetaChip appNamespace={source.namespace} appName={source.name} revision={revision} /></div>}
  </>;
}
