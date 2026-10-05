import { useCallback } from "react";
import { useLocation } from "react-router-dom";
import { ArrowLeft } from "lucide-react";
import type { SelectedResource } from "../../types";
import { WorkloadView } from "../workload/WorkloadView";
import { DATUM_KINDS } from "@skyhook-io/k8s-ui/components/datum/workspace";
import {
  usePinnedDetailContext,
  WorkspaceContextMismatch,
} from "../workspace/detailContext";
import { useWorkspaceNavigate } from "../workspace/useWorkspaceNavigate";
import { currentPageLabel } from "../../utils/page-links";
import {
  datumDetailKindFor,
  datumDetailPath,
  DATUM_SCREENS,
  type DatumTarget,
} from "./routes";
export function DatumDetailPage({
  target,
  namespaces,
  onOpenResource,
}: {
  target: DatumTarget;
  namespaces: string[];
  onOpenResource: (ref: SelectedResource) => void;
}) {
  const { activeContext, pinnedContext, mismatched } = usePinnedDetailContext(),
    navigate = useWorkspaceNavigate(),
    location = useLocation();
  const home = DATUM_SCREENS.find(
    (s) => s.id === DATUM_KINDS[target.plural].home,
  )!;
  const state = location.state as {
    returnLabel?: string;
    returnCtx?: string;
  } | null;
  const returnLabel =
    state?.returnLabel &&
    (!state.returnCtx || state.returnCtx === activeContext)
      ? state.returnLabel
      : null;
  const openRelated = useCallback(
    (ref: SelectedResource) => {
      const plural = datumDetailKindFor(ref.kind, ref.group);
      if (plural)
        navigate(datumDetailPath({ ...ref, plural }, activeContext), {
          state: { returnLabel: currentPageLabel(), returnCtx: activeContext },
        });
      else onOpenResource(ref);
    },
    [navigate, activeContext, onOpenResource],
  );
  if (mismatched)
    return (
      <WorkspaceContextMismatch
        name={target.name}
        pinnedContext={pinnedContext!}
        activeContext={activeContext!}
        homePath={home.path}
        homeLabel={`Datum ${home.label}`}
      />
    );
  const outside =
    !!target.namespace &&
    namespaces.length > 0 &&
    !namespaces.includes(target.namespace);
  return (
    <WorkloadView
      key={`${activeContext}/${target.plural}/${target.namespace}/${target.name}`}
      kind={target.plural}
      namespace={target.namespace}
      name={target.name}
      group={target.group}
      expanded
      onBack={() => (returnLabel ? navigate(-1) : navigate(home.path))}
      hideBackButton
      inlineBadges
      titlePrefix={
        <div className="flex items-center gap-2 text-sm text-theme-text-tertiary">
          {returnLabel && (
            <button
              onClick={() => navigate(-1)}
              className="flex items-center gap-1 max-w-40 truncate text-theme-text-secondary hover:text-theme-text-primary"
            >
              <ArrowLeft className="h-3.5 w-3.5" />
              {returnLabel}
            </button>
          )}
          <span>Datum /</span>
          <button
            className="hover:underline"
            onClick={() => navigate(home.path)}
          >
            {home.label}
          </button>
          <span>/</span>
        </div>
      }
      namespaceNote={
        outside ? (
          <span className="text-xs text-theme-text-tertiary">
            Outside your namespace filter
          </span>
        ) : undefined
      }
      onNavigateToResource={openRelated}
    />
  );
}
