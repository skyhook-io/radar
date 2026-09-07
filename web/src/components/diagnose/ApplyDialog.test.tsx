import type { ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { ApplyDialog } from "./parts";
import { AgentSetupNotice } from "./AgentSetupNotice";

vi.mock("@skyhook-io/k8s-ui/components/ui/DialogPortal", () => ({
  DialogPortal: ({ open, children }: { open: boolean; children: ReactNode }) =>
    open ? children : null,
}));

describe("Apply confirmation context", () => {
  it.each([
    "gke_project-a_us-east1-b_nonprod",
    "gke_project-b_us-east1-b_nonprod",
  ])(
    "identifies the exact destination %s even when display names collide",
    (context) => {
      const html = renderToStaticMarkup(
        <ApplyDialog
          open
          onClose={() => {}}
          onConfirm={() => {}}
          agentLabel="Local agent"
          resourceLabel="Deployment dev/api"
          context={context}
          fix="Only after verifying credentials, update the Secret reference."
        />,
      );
      expect(html).toContain("Cluster:");
      expect(html).toContain("nonprod");
      expect(html).toContain(context);
      expect(html).toContain("Deployment dev/api");
      expect(html).toContain("Proposed change");
      expect(html).not.toContain("What will happen");
      expect(html).not.toContain("max-h-48");
      expect(html).toContain("Only after verifying credentials");
    },
  );

  it("keeps managed-resource acknowledgement and low-confidence warning", () => {
    const html = renderToStaticMarkup(
      <ApplyDialog
        open
        onClose={() => {}}
        onConfirm={() => {}}
        agentLabel="Local agent"
        resourceLabel="Deployment dev/api"
        context="kind-dev"
        fix="Update its configuration"
        managedBy="Argo CD"
        confidence={0.3}
      />,
    );
    expect(html).toContain("I understand Argo CD may revert this");
    const applyButton = html
      .match(/<button\b[^>]*>[\s\S]*?<\/button>/g)
      ?.find((button) => button.includes("Apply fix"));
    expect(applyButton).toContain('disabled=""');
    expect(html).toContain("low confidence");
    expect(html.match(/kubeconfig/g)).toHaveLength(1);
  });
});

it("distinguishes local execution from sending resource data to the model provider", () => {
  const html = renderToStaticMarkup(
    <AgentSetupNotice setupState="needs-install" />,
  );
  expect(html).toContain("Your agent runs locally");
  expect(html).toContain("model provider under your account, not to Radar");
  expect(html).not.toContain("never leaves your machine");
});
