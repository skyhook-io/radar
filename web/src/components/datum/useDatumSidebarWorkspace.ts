import { useMemo } from "react";
import { Globe, Network, Folder, Cable, List } from "lucide-react";
import type { SidebarCategoryWorkspace } from "@skyhook-io/k8s-ui";
import type { APIResource } from "../../types";
import { useRadarFeature } from "../../api/client";
import { useCapabilitiesContext } from "../../contexts/CapabilitiesContext";
import { useWorkspaceNavigate } from "../workspace/useWorkspaceNavigate";
import { DATUM_SCREENS, type DatumScreen } from "./routes";
import { DATUM_KINDS } from "@skyhook-io/k8s-ui/components/datum/workspace";
const icons = {
  hostnames: Globe,
  dns: List,
  connectors: Cable,
  networking: Network,
  projects: Folder,
};
export function useDatumSidebarWorkspace({
  apiResources,
  active,
}: {
  apiResources?: APIResource[];
  active?: { screen: DatumScreen; child?: { label: string } };
}): Record<string, SidebarCategoryWorkspace> | undefined {
  const navigate = useWorkspaceNavigate();
  const { support } = useRadarFeature("datumWorkspace");
  const capabilities = useCapabilitiesContext();
  const local =
    capabilities?.deployment?.mode === "local" && !capabilities?.authEnabled;
  const available =
    local &&
    support === "supported" &&
    apiResources?.some((r) =>
      Object.values(DATUM_KINDS).some((k) => k.group === r.group),
    );
  return useMemo(
    () =>
      available
        ? {
            Datum: {
              destinations: DATUM_SCREENS.filter((s) =>
                apiResources?.some((r) =>
                  Object.values(DATUM_KINDS).some(
                    (k) =>
                      k.group === r.group &&
                      k.kind === r.kind &&
                      k.home === s.id,
                  ),
                ),
              ).map((s) => ({
                id: s.id,
                label: s.label,
                icon: icons[s.id],
                active: active?.screen === s.id,
                child: active?.screen === s.id ? active.child : undefined,
                onSelect: () => navigate(s.path),
              })),
              defaultKindsCollapsed: !!active,
              scopeNote:
                "Namespaced views follow the namespace filter; Projects is control-plane wide",
            },
          }
        : undefined,
    [available, apiResources, active?.screen, active?.child?.label, navigate],
  );
}
