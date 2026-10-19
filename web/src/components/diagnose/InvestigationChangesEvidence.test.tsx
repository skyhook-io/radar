import { renderToStaticMarkup } from "react-dom/server";
import type { ComponentProps } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  DiffViewer,
  ResourceLink,
  type IssueRecentChange,
} from "@skyhook-io/k8s-ui";
import { Tooltip } from "../ui/Tooltip";
import { ChangesBody } from "./investigationEvidence/bodies/streams";
import { EvidenceNavigationContext } from "./investigationEvidence/navigation";
import { recentChange } from "./investigationEvidence/parse";
import { projectInvestigationEvidence } from "./investigationEvidence";

vi.mock("@skyhook-io/k8s-ui", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@skyhook-io/k8s-ui")>();
  return {
    ...actual,
    ResourceLink: vi.fn(actual.ResourceLink),
    DiffViewer: vi.fn((props: ComponentProps<typeof actual.DiffViewer>) => (
      <actual.DiffViewer {...props} />
    )),
  };
});
vi.mock("../ui/Tooltip", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ui/Tooltip")>();
  return { ...actual, Tooltip: vi.fn(actual.Tooltip) };
});

const annotation = {
  workloads: ["Deployment/env-reader", "Deployment/envfrom-reader"],
  pods: 2,
  keys: ["password"],
  note: "These Pods started before Radar observed this write and read the changed keys as env vars. Env values are set when a container starts, so they likely still hold the earlier value until they restart.",
};
const secretChange: IssueRecentChange = {
  kind: "Secret",
  apiVersion: "v1",
  namespace: "secret-feed-proof",
  name: "db-conn",
  changeType: "update",
  summary: "data modified keys: [password]",
  timestamp: "2026-10-19T17:52:12+03:00",
  change_category: "spec_config",
  rank_reason:
    "Secret data changed; 2 Pods likely still hold the earlier value",
  application_configuration_change: true,
  fields: [
    {
      path: "data (modified keys)",
      oldValue: ["password"],
      newValue: ["password"],
    },
  ],
  consumed_by: [
    "Deployment/env-reader",
    "Deployment/envfrom-reader",
    "Deployment/mount-reader",
  ],
  running_previous_value: annotation,
};
const onOpenResource = vi.fn();

function render(change: unknown = secretChange): string {
  const projection = projectInvestigationEvidence(
    [
      {
        timeline: [
          {
            kind: "tool",
            id: "changes",
            tool: "get_changes",
            status: "done",
            summary: JSON.stringify({ namespace: "secret-feed-proof" }),
            result: JSON.stringify({ changes: [change] }),
            isError: false,
            radarEvidence: true,
          },
        ],
      },
    ],
    {
      kind: "Secret",
      group: "",
      namespace: "secret-feed-proof",
      name: "db-conn",
    },
  );
  const data = projection.groups.find((group) => group.kind === "changes")
    ?.latest.data;
  expect(data?.type).toBe("changes");
  if (data?.type !== "changes") throw new Error("Expected a changes card");
  return renderToStaticMarkup(
    <EvidenceNavigationContext.Provider value={{ onOpenResource }}>
      <ChangesBody data={data} />
    </EvidenceNavigationContext.Provider>,
  );
}

beforeEach(() => vi.clearAllMocks());

describe("Secret and ConfigMap change evidence", () => {
  it.each([1, 2])("renders the stale-reader fact for %i Pods", (pods) => {
    const html = render({
      ...secretChange,
      running_previous_value: {
        ...annotation,
        pods,
        keys: ["password", "username"],
      },
    });
    expect(html).toContain(
      pods === 1
        ? "1 Pod likely still holds the earlier value"
        : "2 Pods likely still hold the earlier value",
    );
    expect(html).toMatch(
      /Reads <span[^>]*font-mono[^>]*>password<\/span>, <span[^>]*>username<\/span> as env/,
    );
    expect(html).toContain(annotation.note);
    expect(html).toMatch(/<button[^>]*>Deployment\/env-reader<\/button>/);
    expect(html).toMatch(/<button[^>]*>Deployment\/envfrom-reader<\/button>/);
    expect(html.indexOf(secretChange.summary!)).toBeLessThan(
      html.indexOf("likely still"),
    );
    expect(html.indexOf(annotation.note)).toBeLessThan(
      html.indexOf("1 changed field"),
    );
  });

  it.each([{ workloads: [] }, { workloads: null }])(
    "keeps the Pod count when workload names are hidden (%j)",
    ({ workloads }) => {
      const html = render({
        ...secretChange,
        consumed_by: [],
        running_previous_value: { ...annotation, workloads },
      });
      expect(html).toContain("2 Pods likely still hold the earlier value");
      expect(html).toContain("workload names hidden by your permissions");
      expect(html).not.toContain("Deployment/env-reader");
      expect(
        recentChange({
          ...secretChange,
          running_previous_value: { ...annotation, workloads },
        })?.running_previous_value?.workloads,
      ).toEqual([]);
    },
  );

  it.each([
    ["Deployment", "apps"],
    ["StatefulSet", "apps"],
    ["DaemonSet", "apps"],
    ["ReplicaSet", "apps"],
    ["Job", "batch"],
    ["CronJob", "batch"],
    ["Rollout", "argoproj.io"],
    ["Pod", ""],
  ])("links %s references to the correct API group", (kind, group) => {
    render({
      ...secretChange,
      consumed_by: [`${kind}/reader`],
      running_previous_value: { ...annotation, workloads: [`${kind}/reader`] },
    });
    const links = vi
      .mocked(ResourceLink)
      .mock.calls.map(([props]) => props)
      .filter((props) => props.label === `${kind}/reader`);
    expect(links).toHaveLength(2);
    for (const props of links) {
      expect(props).toMatchObject({
        kind,
        name: "reader",
        namespace: "secret-feed-proof",
        group,
        onNavigate: onOpenResource,
      });
    }
  });

  it("renders Referenced by for a ConfigMap", () => {
    const html = render({
      ...secretChange,
      kind: "ConfigMap",
      running_previous_value: undefined,
    });
    expect(html).toContain("Referenced by");
    expect(html).toMatch(/<button[^>]*>Deployment\/mount-reader<\/button>/);
    expect(html).not.toContain("likely still");
  });

  it("shows collapsed updates beside the timestamp with the first local time in a tooltip", () => {
    const first = "2026-10-19T17:50:00+03:00";
    const html = render({
      ...secretChange,
      updates_in_window: 4,
      first_update_at: first,
    });
    expect(html).toContain("4 updates in this window");
    expect(
      vi.mocked(Tooltip).mock.calls.map(([props]) => props.content),
    ).toContain(`First update: ${new Date(first).toLocaleString()}`);
    expect(html.indexOf("4 updates in this window")).toBeLessThan(
      html.indexOf("<time"),
    );
  });

  it.each([undefined, 1])(
    "omits the collapsed-update label for %s",
    (updates_in_window) => {
      expect(render({ ...secretChange, updates_in_window })).not.toContain(
        "updates in this window",
      );
    },
  );

  it.each(
    [
      null,
      [],
      "malformed",
      { ...annotation, workloads: "Deployment/env-reader" },
      { ...annotation, workloads: [1] },
      { ...annotation, pods: "2" },
      { ...annotation, pods: -1 },
      { ...annotation, pods: 1.5 },
      { ...annotation, keys: null },
      { ...annotation, keys: [1] },
      { ...annotation, note: false },
      { pods: 2, keys: ["password"], note: annotation.note },
    ].map((running_previous_value) => ({ running_previous_value })),
  )(
    "drops only a malformed stale-reader annotation (%j)",
    ({ running_previous_value }) => {
      const row = { ...secretChange, running_previous_value };
      expect(recentChange(row)).not.toHaveProperty("running_previous_value");
      const html = render(row);
      expect(html).toContain(secretChange.summary);
      expect(html).toContain("Referenced by");
      expect(html).not.toContain("likely still");
    },
  );

  it.each([0, -1, 1.5, "4", null, Infinity, NaN])(
    "drops an invalid update count (%j) without losing the row",
    (updates_in_window) => {
      const row = { ...secretChange, updates_in_window };
      expect(recentChange(row)).not.toHaveProperty("updates_in_window");
      const html = render(row);
      expect(html).toContain(secretChange.summary);
      expect(html).toContain(annotation.note);
      expect(html).not.toContain("updates in this window");
    },
  );

  it.each([null, 3, {}, []].map((first_update_at) => ({ first_update_at })))(
    "drops a non-string first update time (%j)",
    ({ first_update_at }) => {
      const row = { ...secretChange, updates_in_window: 3, first_update_at };
      expect(recentChange(row)).not.toHaveProperty("first_update_at");
      expect(render(row)).toContain("3 updates in this window");
    },
  );

  it("renders only key-name lists in the example Secret diff", () => {
    const html = render();
    expect(secretChange.fields).toEqual([
      {
        path: "data (modified keys)",
        oldValue: ["password"],
        newValue: ["password"],
      },
    ]);
    expect(vi.mocked(DiffViewer).mock.calls[0][0].diff.fields).toEqual(
      secretChange.fields,
    );
    expect(html.match(/\[&quot;password&quot;\]/g)).toHaveLength(2);
    expect(html).toContain(">password</span>");
    expect(html).not.toMatch(/oldValue|newValue|stringData/);
  });
});
