import { describe, expect, it } from "vitest";
import { renderToString } from "react-dom/server";
import { TopologyFilterSidebar } from "./TopologyFilterSidebar";

describe("TopologyFilterSidebar Calico policy category", () => {
  it.each([
    "CalicoNetworkPolicy",
    "CalicoGlobalNetworkPolicy",
    "CalicoStagedNetworkPolicy",
    "CalicoStagedGlobalNetworkPolicy",
    "CalicoStagedKubernetesNetworkPolicy",
  ])("places %s under Custom Resources with its kind color", (kind) => {
    const html = renderToString(
      <TopologyFilterSidebar
        nodes={[
          { id: "policy", kind, name: "policy", status: "healthy", data: {} },
        ]}
        visibleKinds={new Set([kind])}
        onToggleKind={() => {}}
        onShowAll={() => {}}
        onHideAll={() => {}}
      />,
    );

    expect(html).toContain("Custom Resources");
    expect(html).not.toContain(">Networking<");
    expect(html).toContain(
      kind.includes("Staged") ? "text-amber-400" : "text-teal-400",
    );
  });
});
