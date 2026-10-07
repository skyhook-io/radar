// @vitest-environment jsdom
import type { ReactNode } from "react";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { evaluateGitOpsWriteGuard } from "@skyhook-io/k8s-ui/utils/gitops-write-guard";
import { ApplyDialog } from "./parts";

vi.mock("@skyhook-io/k8s-ui/components/ui/DialogPortal", () => ({
  DialogPortal: ({ open, children }: { open: boolean; children: ReactNode }) => (open ? children : null),
}));

const target = { kind: "Deployment", group: "apps", namespace: "dev", name: "api" };
const owner = { tool: "argocd" as const, kind: "applications" as const, namespace: "argocd", name: "api" };
const writes = [{ scope: "spec" as const }];
// Ownership unknown at first; then the owner resolves to an Argo CD Application.
const guardWith = (resolved: boolean) =>
  resolved
    ? evaluateGitOpsWriteGuard({ target, owner, writes, evidence: { uid: "u", resourceVersion: "1", owner: null, policy: { tool: "argocd", auto: true, selfHeal: true, prune: false, suspended: null }, paths: [] } })
    : evaluateGitOpsWriteGuard({ target, owner: null, writes, ownershipError: "its relationships aren't mapped yet" });

let container: HTMLDivElement;
afterEach(() => container?.remove());

it("asks again when the verdict changes after the user acknowledged", async () => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  container = document.createElement("div");
  document.body.appendChild(container);
  const root = createRoot(container);
  const render = (resolved: boolean) =>
    root.render(
      <ApplyDialog open onClose={() => {}} onConfirm={() => {}} agentLabel="Local agent" resourceLabel="Deployment dev/api" context="kind-dev" fix="Raise the memory limit." gitOpsGuard={guardWith(resolved)} />,
    );
  const applyButton = () => [...container.querySelectorAll("button")].find((b) => b.textContent?.includes("Apply fix"))!;
  try {
    await act(async () => render(false));
    expect(applyButton().disabled).toBe(true);
    const checkbox = container.querySelector<HTMLInputElement>('input[type="checkbox"]')!;
    await act(async () => checkbox.click());
    expect(applyButton().disabled).toBe(false);
    await act(async () => render(true));
    expect(applyButton().disabled).toBe(true);
    expect(container.querySelector<HTMLInputElement>('input[type="checkbox"]')!.checked).toBe(false);
  } finally {
    await act(async () => root.unmount());
  }
});
