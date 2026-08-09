import { useLocation, useNavigate } from "react-router-dom";
import { useCapacityOverview } from "../../api/client";
import { useConnection } from "../../context/ConnectionContext";
import type { SelectedResource } from "../../types";
import { CapacityActivity } from "./CapacityActivity";
import { CapacityDemand } from "./CapacityDemand";
import { CapacityOverview } from "./CapacityOverview";
import { CapacityPoolDetail } from "./CapacityPoolDetail";
import { capacityPoolPath, parseCapacityRoute } from "./shared";

interface CapacityViewProps {
  onOpenResource: (resource: SelectedResource) => void;
}

/**
 * Capacity is a hub-and-spoke surface: Overview is the landing hub; Demand,
 * Activity and NodePool detail are spokes reached from the signals, counts and
 * pools that reference them (each carries a breadcrumb back). No persistent tab
 * chrome — the destination you're on is the page.
 */
/**
 * Capacity is deliberately cluster-wide: supply (pools/nodes/claims) is
 * cluster-scoped and unfilterable, so scoping only the pod-derived numbers
 * would show "my namespace's demand" against "everyone's supply" — the wrong
 * mental model for capacity questions. RBAC (involuntary, honestly labeled in
 * coverage) is the only scoper; the header's namespace view filter does not
 * apply here and is not forwarded.
 */
export function CapacityView({ onOpenResource }: CapacityViewProps) {
  const location = useLocation();
  const navigate = useNavigate();
  const route = parseCapacityRoute(location.pathname);
  const { connection } = useConnection();
  const overview = useCapacityOverview({
    enabled: !route.poolName && route.topTab === "overview",
  });

  const navigateCapacity = (target: string) => {
    const [pathname, rawSearch = ""] = target.split("?", 2);
    const params = new URLSearchParams(rawSearch);
    navigate({
      pathname,
      search: params.toString() ? `?${params.toString()}` : "",
    });
  };

  const openPool = (name: string) => navigateCapacity(capacityPoolPath(name));

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col bg-theme-base">
      {route.poolName ? (
        <CapacityPoolDetail
          key={route.poolName}
          name={route.poolName}
          section={route.poolSection}
          connectionState={connection.state}
          onNavigate={navigateCapacity}
          onOpenResource={onOpenResource}
        />
      ) : route.topTab === "overview" ? (
        <CapacityOverview
          query={overview}
          connectionState={connection.state}
          onOpenPool={openPool}
          onOpenResource={onOpenResource}
          onNavigate={navigateCapacity}
        />
      ) : route.topTab === "demand" ? (
        <CapacityDemand
          connectionState={connection.state}
          onOpenPool={openPool}
          onOpenResource={onOpenResource}
          onNavigate={navigateCapacity}
        />
      ) : (
        <CapacityActivity
          connectionState={connection.state}
          onOpenPool={openPool}
          onOpenResource={onOpenResource}
          onNavigate={navigateCapacity}
        />
      )}
    </div>
  );
}
