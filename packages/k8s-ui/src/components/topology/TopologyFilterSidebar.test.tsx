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

it('separates synthetic pod groups from actual PodGroup APIs', () => {
  const html = renderToString(<TopologyFilterSidebar nodes={[
    {id:'aggregate',kind:'PodGroup',name:'web',status:'healthy',data:{namespace:'team',podCount:7}},
    {id:'batch',kind:'PodGroup',name:'batch',status:'healthy',data:{namespace:'team',apiVersion:'scheduling.volcano.sh/v1beta1'}}
  ]} visibleKinds={new Set(['PodGroup','PodGroup@scheduling.volcano.sh'])} onToggleKind={()=>{}} onShowAll={()=>{}} onHideAll={()=>{}} />)
  expect(html).toContain('Pod Group')
  expect(html).toContain('PodGroup · scheduling.volcano.sh')
  expect(html).toContain('Custom Resources')
})
