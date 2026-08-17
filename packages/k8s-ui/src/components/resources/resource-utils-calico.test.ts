import { describe, expect, it } from "vitest";
import {
  getCalicoApiGroup,
  getCalicoPolicyKindLabel,
  getCalicoPolicySelector,
  getCalicoTierRef,
  isCalicoApiVersion,
  isCalicoPolicyResource,
  isCalicoStagedKubernetesNetworkPolicyKind,
  isCoreNetworkPolicyKind,
} from "./resource-utils-calico";

describe("Calico API group matching", () => {
  it("extracts only supported Calico API groups", () => {
    expect(getCalicoApiGroup("projectcalico.org/v3")).toBe("projectcalico.org");
    expect(getCalicoApiGroup("crd.projectcalico.org/v1")).toBe(
      "crd.projectcalico.org",
    );
    expect(getCalicoApiGroup("extension.projectcalico.org/v1")).toBeUndefined();
  });

  it("builds a cluster-scoped Tier reference with the policy API group", () => {
    expect(
      getCalicoTierRef({
        apiVersion: "crd.projectcalico.org/v1",
        spec: { tier: "security" },
      }),
    ).toEqual({
      kind: "Tier",
      namespace: "",
      name: "security",
      group: "crd.projectcalico.org",
    });
  });

  it.each(["projectcalico.org/v3", "crd.projectcalico.org/v1"])(
    "accepts the exact Calico group: %s",
    (apiVersion) => {
      expect(isCalicoApiVersion(apiVersion)).toBe(true);
      expect(
        isCalicoPolicyResource({ apiVersion, kind: "NetworkPolicy" }),
      ).toBe(true);
    },
  );

  it.each(["projectcalico.org/v3", "crd.projectcalico.org/v1"])(
    "recognizes staged Kubernetes network policies in %s",
    (apiVersion) => {
      const policy = {
        apiVersion,
        kind: "StagedKubernetesNetworkPolicy",
        spec: {
          podSelector: { matchLabels: { app: "frontend" } },
          policyTypes: ["Ingress"],
        },
      };
      expect(isCalicoStagedKubernetesNetworkPolicyKind(policy.kind)).toBe(true);
      expect(isCalicoPolicyResource(policy)).toBe(true);
      expect(getCalicoPolicySelector(policy)).toBe("app=frontend");
      expect(getCalicoPolicyKindLabel(policy.kind)).toBe(
        "CalicoStagedKubernetesNetworkPolicy",
      );
    },
  );

  it.each([
    "extension.projectcalico.org/v1",
    "projectcalico.org.evil/v1",
    "networking.example.io/v1",
    "v1",
  ])("rejects a non-Calico group: %s", (apiVersion) => {
    expect(isCalicoApiVersion(apiVersion)).toBe(false);
    expect(isCalicoPolicyResource({ apiVersion, kind: "NetworkPolicy" })).toBe(
      false,
    );
  });

  it("recognizes only the core networking.k8s.io policy", () => {
    expect(
      isCoreNetworkPolicyKind("NetworkPolicy", "networking.k8s.io/v1"),
    ).toBe(true);
    expect(
      isCoreNetworkPolicyKind("NetworkPolicy", "projectcalico.org/v3"),
    ).toBe(false);
    expect(
      isCoreNetworkPolicyKind("NetworkPolicy", "other.example.io/v1"),
    ).toBe(false);
    expect(isCoreNetworkPolicyKind("NetworkPolicy", undefined)).toBe(true);
    expect(
      isCoreNetworkPolicyKind("NetworkPolicy", undefined, "networking.k8s.io"),
    ).toBe(true);
    expect(
      isCoreNetworkPolicyKind("NetworkPolicy", undefined, "other.example.io"),
    ).toBe(false);
  });

  it("does not let the exact core fallback classify foreign kinds", () => {
    expect(
      isCoreNetworkPolicyKind("GlobalNetworkPolicy", "networking.k8s.io/v1"),
    ).toBe(false);
    expect(isCoreNetworkPolicyKind("Widget", undefined)).toBe(false);
  });
});
