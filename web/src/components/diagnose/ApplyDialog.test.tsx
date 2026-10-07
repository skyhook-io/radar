import type { ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { ApplyDialog } from "./parts";
import { AgentSetupNotice } from "./AgentSetupNotice";
import { evaluateGitOpsWriteGuard } from "@skyhook-io/k8s-ui/utils/gitops-write-guard";

const target = { kind: "Deployment", group: "apps", namespace: "dev", name: "api" };
const APPLY_WRITES = [{ scope: "spec" as const }];
const unmanaged = evaluateGitOpsWriteGuard({ target, owner: null, writes: APPLY_WRITES });
const argoManaged = evaluateGitOpsWriteGuard({
  target,
  owner: { tool: "argocd", kind: "applications", namespace: "argocd", name: "api" },
  writes: APPLY_WRITES,
  evidence: {
    uid: "u",
    resourceVersion: "1",
    owner: null,
    policy: { tool: "argocd", auto: true, selfHeal: true, prune: false, suspended: null },
    paths: [],
  },
});

function applyButton(html: string) {
  return html
    .match(/<button\b[^>]*>[\s\S]*?<\/button>/g)
    ?.find((button) => button.includes("Apply fix"));
}

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
          gitOpsGuard={unmanaged}
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
        gitOpsGuard={argoManaged}
        confidence={0.3}
      />,
    );
    expect(html).toContain("Argo CD Application argocd/api may revert this change.");
    expect(html).toContain("I understand Argo CD may revert this");
    expect(applyButton(html)).toContain('disabled=""');
    expect(html).toContain("low confidence");
    expect(html.match(/kubeconfig/g)).toHaveLength(1);
  });
});

describe("Apply GitOps gate", () => {
  const props = {
    open: true,
    onClose: () => {},
    onConfirm: () => {},
    agentLabel: "Local agent",
    resourceLabel: "Deployment dev/api",
    context: "kind-dev",
  };

  it("enables Apply for an unmanaged target without acknowledgement", () => {
    const html = renderToStaticMarkup(<ApplyDialog {...props} gitOpsGuard={unmanaged} />);
    expect(applyButton(html)).not.toContain('disabled=""');
    expect(html).not.toContain('type="checkbox"');
  });

  it("blocks Apply while ownership is being checked or unknown", () => {
    const pending = evaluateGitOpsWriteGuard({ target, owner: null, ownerPending: true, writes: APPLY_WRITES });
    const checking = renderToStaticMarkup(<ApplyDialog {...props} gitOpsGuard={pending} />);
    expect(checking).toContain("Checking GitOps ownership");
    expect(applyButton(checking)).toContain('disabled=""');
    expect(applyButton(renderToStaticMarkup(<ApplyDialog {...props} />))).toContain('disabled=""');
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
