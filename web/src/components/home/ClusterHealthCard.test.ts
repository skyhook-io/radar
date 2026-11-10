import { describe, expect, it } from "vitest";
import { clusterIdentity, minorsBehind } from "./ClusterHealthCard";

describe("minorsBehind", () => {
  it("counts minors between plain versions", () => {
    expect(minorsBehind("1.33", "1.37")).toBe(4);
    expect(minorsBehind("1.36", "1.37")).toBe(1);
  });

  it("returns 0 when current matches or exceeds the catalog", () => {
    expect(minorsBehind("1.37", "1.37")).toBe(0);
    expect(minorsBehind("1.38", "1.37")).toBe(0);
  });

  it("handles distribution version suffixes", () => {
    expect(minorsBehind("v1.33.6+k3s1", "1.37")).toBe(4);
    expect(minorsBehind("1.31.5-gke.1023000", "1.37")).toBe(6);
    expect(minorsBehind("v1.32.4-eks-abc123", "1.37")).toBe(5);
  });

  it("stays quiet on cross-major or unparseable input", () => {
    expect(minorsBehind("2.0.0", "1.37")).toBe(0);
    expect(minorsBehind("unknown", "1.37")).toBe(0);
    expect(minorsBehind("", "1.37")).toBe(0);
  });

  it("stays quiet when the catalog version is missing", () => {
    expect(minorsBehind("1.33", undefined)).toBe(0);
    expect(minorsBehind("1.33", "")).toBe(0);
  });
});

describe("clusterIdentity", () => {
  it("names contexts that share one kubeconfig cluster entry apart", () => {
    const a = clusterIdentity({ name: "agent-a", kubeconfigCluster: "gitlab_com" });
    const b = clusterIdentity({ name: "agent-b", kubeconfigCluster: "gitlab_com" });
    expect(a.headline).toBe("agent-a");
    expect(b.headline).toBe("agent-b");
    expect(a.raw).toBe("agent-a");
  });

  it("shortens cloud contexts the same way the switcher does", () => {
    const id = clusterIdentity({ name: "gke_proj_us-east1-b_my-cluster", kubeconfigCluster: "gke_proj_us-east1-b_my-cluster" });
    expect(id.headline).toBe("my-cluster");
    expect(id.location).toBe("proj · us-east1-b");
  });

  it("shows the server host for an OpenShift context, keeping the full context as raw", () => {
    const id = clusterIdentity({ name: "myproject/api-prod-example-com:6443/alice", kubeconfigCluster: "api-prod-example-com:6443" });
    expect(id.headline).toBe("api-prod-example-com");
    expect(id.raw).toBe("myproject/api-prod-example-com:6443/alice");
    expect(id.location).toBe("");
  });

  it("keeps the cluster entry's account and region for a renamed cloud context", () => {
    const id = clusterIdentity({ name: "prod-eks-alias", kubeconfigCluster: "arn:aws:eks:us-west-2:123456789012:cluster/orders" });
    expect(id.headline).toBe("prod-eks-alias");
    expect(id.location).toBe("123456789012 · us-west-2");
  });

  it("shows no location when neither name is a cloud name", () => {
    expect(clusterIdentity({ name: "group/project:agent", kubeconfigCluster: "gitlab_com" }).location).toBe("");
  });

  it("reads an older Radar that sends only the cluster entry as name", () => {
    expect(clusterIdentity({ name: "gitlab_com" }).headline).toBe("gitlab_com");
    expect(clusterIdentity({ name: "arn:aws:eks:us-west-2:123456789012:cluster/orders" }).location).toBe("123456789012 · us-west-2");
  });

  it("falls back to Cluster with no name", () => {
    expect(clusterIdentity({ name: "in-cluster" }).headline).toBe("in-cluster");
    expect(clusterIdentity({ name: "" }).headline).toBe("Cluster");
  });
});
