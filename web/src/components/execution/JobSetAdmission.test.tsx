import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { KueueAdmission } from "./JobSetAdmission";
const mock = vi.hoisted(() => ({ result: {} as any, calls: [] as any[] }));
vi.mock("../../api/client", () => ({
  ApiError: class extends Error {
    status = 403;
  },
  useKueueAdmission: (...args: any[]) => {
    mock.calls.push(args);
    return mock.result;
  },
}));
const job = {
  apiVersion: "batch/v1",
  kind: "Job",
  metadata: { name: "train", namespace: "ml", uid: "current" },
  spec: { suspend: true },
};
const empty = {
  uid: "current",
  installed: true,
  workloads: [],
  total: 0,
  truncated: false,
};
const evidence = {
  ...empty,
  total: 1,
  workloads: [
    {
      name: "owned-workload",
      uid: "w",
      apiVersion: "kueue.x-k8s.io/v1beta2",
      projection: "unsupported",
    },
  ],
};
const hinted = {
  ...job,
  metadata: {
    ...job.metadata,
    labels: { "kueue.x-k8s.io/queue-name": "ready" },
  },
};
const render = (resource: any = job) =>
  renderToStaticMarkup(
    <KueueAdmission resource={resource} namespace="ml" name="train" />,
  );
describe("Job admission host", () => {
  beforeEach(() => {
    mock.result = {};
    mock.calls = [];
  });
  it.each([
    { isLoading: true },
    { data: empty },
    { error: new Error("denied") },
  ])("keeps plain suspended Jobs quiet: %j", (result) => {
    mock.result = result;
    expect(render()).toBe("");
    expect(mock.calls[0][3]).toEqual({
      isJob: true,
      hinted: false,
      terminal: false,
    });
  });
  it("discovers unlabeled evidence but hides it after errors", () => {
    mock.result = { data: evidence };
    expect(render()).toContain("owned-workload");
    mock.result.error = new Error("cache unavailable");
    expect(render()).toContain("cache unavailable");
    expect(render()).not.toContain("owned-workload");
  });
  it("rejects reused-name evidence", () => {
    mock.result = { data: { ...evidence, uid: "old" } };
    expect(render(hinted)).toContain("different workload instance");
    expect(render(hinted)).not.toContain("owned-workload");
  });
  it("shows missing evidence and conditional owner guidance for a hinted child", () => {
    mock.result = { data: empty };
    const html = render({
      ...hinted,
      metadata: {
        ...hinted.metadata,
        ownerReferences: [{ controller: true, kind: "JobSet", name: "parent" }],
      },
    });
    expect(html).toContain("Admission may be tracked on the owning resource");
    expect(html).not.toContain("Admission is tracked");
  });
  it("does not use failed Pod counts as terminal or arbitrary managedBy as Kueue hint", () => {
    render({
      ...job,
      spec: { managedBy: "other.io/controller" },
      status: { failed: 1 },
    });
    expect(mock.calls[0][3]).toEqual({
      isJob: true,
      hinted: false,
      terminal: false,
    });
  });
  it("preserves JobSet errors", () => {
    mock.result = { error: new Error("denied") };
    expect(
      render({
        ...job,
        apiVersion: "jobset.x-k8s.io/v1alpha2",
        kind: "JobSet",
      }),
    ).toContain("denied");
  });
});

describe("JobSet admission identity", () => {
  it("rejects evidence and absence for a different root instance", () => {
    const resource = {
      ...job,
      apiVersion: "jobset.x-k8s.io/v1alpha2",
      kind: "JobSet",
    };
    mock.result = { data: evidence };
    expect(render(resource)).toContain("owned-workload");
    mock.result = { data: { ...evidence, uid: "old" } };
    expect(render(resource)).toContain("different workload instance");
    expect(render(resource)).not.toContain("owned-workload");
    mock.result = { data: { ...empty, uid: "old", installed: false } };
    expect(render(resource)).toContain("different workload instance");
    mock.result.data.uid = "current";
    expect(render(resource)).toBe("");
  });
});

it("does not suggest parent admission for a CronJob-created Job", () => {
  mock.result = { data: empty };
  const html = render({
    ...hinted,
    metadata: {
      ...hinted.metadata,
      ownerReferences: [
        { apiVersion: "batch/v1", kind: "CronJob", controller: true },
      ],
    },
  });
  expect(html).toContain("No controller-owned Kueue Workload observed");
  expect(html).not.toContain("Admission may be tracked");
});
