import { useEffect } from "react";
import { useLocation, useSearchParams } from "react-router-dom";
import { Globe } from "lucide-react";
import {
  ResourcesSidebar,
  PaneLoader,
  type SelectedKindInfo,
} from "@skyhook-io/k8s-ui";
import { useAPIResources } from "../../api/apiResources";
import { useCapabilitiesContext } from "../../contexts/CapabilitiesContext";
import { useDatumWorkspace } from "../../api/datum";
import { usePinnedKinds } from "../../hooks/useFavorites";
import { useResourceCounts } from "../../hooks/useResourceCounts";
import { useWorkspaceDrawer } from "../workspace/useWorkspaceDrawer";
import { useWorkspaceNavigate } from "../workspace/useWorkspaceNavigate";
import { ScreenEmptyState } from "../workspace/layout";
import { useDatumSidebarWorkspace } from "./useDatumSidebarWorkspace";
import { parseDatumRoute } from "./routes";
import { DatumDetailPage } from "./DatumDetailPage";
import { DatumScreens } from "./DatumScreens";
import type { SelectedResource } from "../../types";
export function DatumView({
  namespaces,
  selectedResource,
  onOpenResource,
  onCloseResource,
  onClearNamespaces,
}: {
  namespaces: string[];
  selectedResource: SelectedResource | null;
  onOpenResource: (r: SelectedResource) => void;
  onCloseResource: () => void;
  onClearNamespaces: () => void;
}) {
  const location = useLocation(),
    navigate = useWorkspaceNavigate(),
    [params, setParams] = useSearchParams();
  const route = parseDatumRoute(location.pathname),
    { data: apiResources } = useAPIResources(),
    { data: counts } = useResourceCounts(namespaces),
    { pinned, togglePin, isPinned } = usePinnedKinds();
  const workspace = useDatumSidebarWorkspace({
    apiResources,
    active: {
      screen: route.screen,
      child: route.detail ? { label: route.detail.name } : undefined,
    },
  });
  const capabilities = useCapabilitiesContext();
  const local =
    capabilities.deployment?.mode === "local" && !capabilities.authEnabled;
  const query = useDatumWorkspace(namespaces, !route.detail && local),
    { drawerTarget, inspect } = useWorkspaceDrawer(
      selectedResource,
      onOpenResource,
      onCloseResource,
    );
  useEffect(() => {
    if (
      route.screen === "projects" &&
      query.data?.installed &&
      query.data.coverage.projects?.state === "notInstalled"
    ) {
      navigate("/datum", { replace: true });
    }
  }, [route.screen, query.data, navigate]);
  const selectKind = (kind: SelectedKindInfo) =>
    navigate(
      `/resources/${kind.name}${kind.group ? `?apiGroup=${encodeURIComponent(kind.group)}` : ""}`,
    );
  return (
    <div className="flex h-full min-h-0 w-full">
      <ResourcesSidebar
        selectedKind={null}
        onSelectedKindChange={selectKind}
        apiResources={apiResources}
        resourceCounts={counts?.counts}
        resourceForbidden={counts?.forbidden}
        resourceUnavailable={counts?.unavailable}
        pinned={pinned}
        togglePin={togglePin}
        isPinned={(kind, group) => isPinned(kind, group || "")}
        categoryWorkspaces={workspace}
      />
      <div className="flex min-h-0 min-w-0 flex-1 flex-col bg-theme-base">
        {!local ? (
          <ScreenEmptyState
            icon={Globe}
            title="Datum workspace is available in local Radar only"
            detail="Open local Radar with your own kubeconfig and authentication disabled."
          />
        ) : route.detail ? (
          <DatumDetailPage
            target={route.detail}
            namespaces={namespaces}
            onOpenResource={onOpenResource}
          />
        ) : query.isLoading ? (
          <PaneLoader label="Loading Datum inventory…" className="h-full" />
        ) : query.error ? (
          <ScreenEmptyState
            icon={Globe}
            title="Datum inventory could not be read"
            detail={query.error.message}
          />
        ) : !query.data ||
          (!query.data.installed &&
            Object.values(query.data.coverage).every(
              (c) => c.state === "notInstalled",
            )) ? (
          <ScreenEmptyState
            icon={Globe}
            title="Datum APIs not discovered"
            detail="Open a control plane that serves the Datum or Milo ResourceManager APIs."
          />
        ) : (
          <DatumScreens
            data={query.data}
            screen={route.screen}
            onInspect={inspect}
            inspected={drawerTarget}
            namespaces={namespaces}
            onClearNamespaces={onClearNamespaces}
            attentionOnly={params.get("filter") === "attention"}
            onAttentionChange={(v) => {
              const next = new URLSearchParams(params);
              if (v) next.set("filter", "attention");
              else next.delete("filter");
              setParams(next, { replace: true, state: location.state });
            }}
          />
        )}
      </div>
    </div>
  );
}
