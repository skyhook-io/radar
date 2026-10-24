import { renderToStaticMarkup } from "react-dom/server";
import type { ComponentProps } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  DiffViewer,
  ResourceLink,
  formatRelativeAgeTime,
  type IssueRecentChange,
} from "@skyhook-io/k8s-ui";
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
      /Changed keys read as env: <span[^>]*font-mono[^>]*>password<\/span>, <span[^>]*>username<\/span><\/p>/,
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

  it("marks a full list of five references and stale workloads as capped", () => {
    const five = ["a", "b", "c", "d", "e"].map((name) => `Deployment/${name}`);
    const html = render({
      ...secretChange,
      consumed_by: five,
      running_previous_value: { ...annotation, pods: 7, workloads: five },
    });
    expect(html.match(/\(first 5\)/g)).toHaveLength(2);
    expect(html).toContain("In workloads you can view:");
    expect(render(secretChange)).not.toContain("(first 5)");
  });

  it("renders references as text when the row has no namespace", () => {
    render({ ...secretChange, namespace: undefined });
    expect(
      vi
        .mocked(ResourceLink)
        .mock.calls.filter(([props]) =>
          String(props.label).startsWith("Deployment/"),
        ),
    ).toHaveLength(0);
  });

  it("counts names the caller can't view instead of hiding them silently", () => {
    const withheld = { count: 2, reason: "user_forbidden" };
    const some = render({
      ...secretChange,
      consumed_by: ["Deployment/env-reader"],
      consumed_by_withheld: withheld,
      running_previous_value: {
        ...annotation,
        workloads: ["Deployment/env-reader"],
        withheld,
      },
    });
    expect(some.match(/\+2 more you can(&#x27;|')t view/g)).toHaveLength(2);
    const none = render({
      ...secretChange,
      consumed_by: undefined,
      consumed_by_withheld: withheld,
      running_previous_value: { ...annotation, workloads: [], withheld },
    });
    expect(none).toContain("Referenced by");
    expect(none).toContain("2 workloads you can&#x27;t view");
    expect(none).toContain("In 2 workloads you can&#x27;t view");
    expect(none).not.toContain("hidden by your permissions");
  });

  it.each([{ count: 0, reason: "user_forbidden" }, { count: 1 }, "two", null])(
    "drops a malformed withheld marker (%j) and keeps the row",
    (marker) => {
      const row = recentChange({
        ...secretChange,
        consumed_by_withheld: marker,
        running_previous_value: { ...annotation, withheld: marker },
      });
      expect(row).not.toHaveProperty("consumed_by_withheld");
      expect(row?.running_previous_value).not.toHaveProperty("withheld");
      expect(row?.summary).toBe(secretChange.summary);
    },
  );

  it("renders a reference of a kind with no known group as text, not a link", () => {
    const html = render({
      ...secretChange,
      consumed_by: ["Cluster/database"],
      running_previous_value: { ...annotation, workloads: ["Cluster/database"] },
    });
    expect(html).toContain("Cluster/database");
    expect(
      vi
        .mocked(ResourceLink)
        .mock.calls.filter(([props]) => props.label === "Cluster/database"),
    ).toHaveLength(0);
  });

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

  it("shows collapsed updates and the first update's age beside the timestamp", () => {
    const first = "2026-10-19T17:50:00+03:00";
    const html = render({
      ...secretChange,
      updates_in_window: 4,
      first_update_at: first,
    });
    const label = `4 updates, first ${formatRelativeAgeTime(first)}`;
    expect(html).toContain(label);
    expect(html.indexOf(label)).toBeLessThan(html.indexOf("<time"));
  });

  it.each([undefined, 1])(
    "omits the collapsed-update label for %s",
    (updates_in_window) => {
      expect(render({ ...secretChange, updates_in_window })).not.toContain(
        " updates",
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
      expect(html).not.toMatch(/\d updates/);
    },
  );

  it.each([null, 3, {}, [], "not a time"].map((first_update_at) => ({ first_update_at })))(
    "drops a non-string first update time (%j)",
    ({ first_update_at }) => {
      const row = { ...secretChange, updates_in_window: 3, first_update_at };
      expect(recentChange(row)).not.toHaveProperty("first_update_at");
      expect(render(row)).toMatch(/>3 updates</);
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
    // The key list reads as a key change, not a struck-through old→new pair.
    expect(html).not.toContain("[&quot;password&quot;]");
    expect(html).toContain("keys changed: ");
    expect(html).toContain(">password</span>");
    expect(html).not.toMatch(/oldValue|newValue|stringData/);
  });
});
