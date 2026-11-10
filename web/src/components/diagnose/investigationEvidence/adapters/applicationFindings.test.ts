import { describe, expect, it } from "vitest";
import {
  investigationEvidenceSubjectRef,
  projectInvestigationEvidence,
} from "../index";
import {
  criticalIssue,
  deployment,
  evidenceRef,
  groupsOf,
  project,
  target,
  tool,
} from "../evidenceFixtures";

const connector = {
  ...criticalIssue,
  id: "kafka.strimzi.io:KafkaConnector:shop:sink:StrimziConnectorTasksFailed",
  severity: "warning",
  group: "kafka.strimzi.io",
  kind: "KafkaConnector",
  name: "sink",
  reason: "StrimziConnectorTasksFailed",
  message: "Strimzi reports one or more connector tasks as FAILED",
};
const source = {
  group: "kafka.strimzi.io",
  kind: "KafkaConnect",
  namespace: "shop",
  name: "connect",
};
const context = {
  tier: "basic",
  workloadSummary: { replicas: { desired: 1, ready: 1, available: 1 } },
  relatedFindings: [
    {
      kinds: {
        count: 1,
        names: [{ kind: "KafkaConnector", group: "kafka.strimzi.io" }],
      },
      associatedVia: source,
      gaps: [],
      checked: 1,
      unknown: 0,
      findings: [connector],
    },
  ],
};

describe("cached application findings", () => {
  it("accepts an empty group list and does not invent a limitation", () => {
    const result = project([
      tool("read", "diagnose", {
        resource: deployment,
        relatedIssues: [],
        resourceContext: { ...context, relatedFindings: [] },
      }),
    ]);
    expect(groupsOf(result.groups, "issue")).toHaveLength(0);
    expect(result.limitations).toHaveLength(0);
    expect(
      groupsOf(result.groups, "receipt").some(
        (group) =>
          group.latest.data.type === "receipt" &&
          group.latest.data.checked === "issues",
      ),
    ).toBe(true);
  });

  it("retains good groups around a malformed group and preserves both anchors for one issue", () => {
    const first = context.relatedFindings[0];
    const second = {
      ...first,
      associatedVia: {
        group: "argoproj.io",
        kind: "Application",
        namespace: "ops",
        name: "store",
      },
      unknown: 1,
    };
    const result = project([
      tool("read", "get_resource", {
        resource: deployment,
        resourceContext: {
          ...context,
          relatedFindings: [
            first,
            { ...first, kinds: { count: 2, names: [] } },
            second,
          ],
        },
      }),
    ]);
    const groups = groupsOf(result.groups, "issue");
    expect(groups).toHaveLength(1);
    expect(groups[0].observations).toHaveLength(2);
    expect(
      groups[0].observations.map(
        (item) => item.data.type === "issue" && item.data.associatedVia,
      ),
    ).toEqual([source, second.associatedVia]);
    expect(
      result.limitations.some(
        (item) => item.source === "Related findings group",
      ),
    ).toBe(true);
    expect(
      result.limitations.some(
        (item) =>
          item.source.includes("Application ops/store") &&
          item.kind === "unknown",
      ),
    ).toBe(true);
  });

  it("retains distinct issue IDs on one subject across repeated anchors", () => {
    const first = context.relatedFindings[0];
    const other = {
      ...connector,
      id: `${connector.id}:other`,
      reason: "OtherFailure",
    };
    const result = project([
      tool("read", "get_resource", {
        resource: deployment,
        resourceContext: {
          ...context,
          relatedFindings: [
            { ...first, findings: [connector, other] },
            {
              ...first,
              associatedVia: { ...source, name: "second" },
              findings: [connector, other],
            },
          ],
        },
      }),
    ]);
    const groups = groupsOf(result.groups, "issue");
    expect(groups).toHaveLength(2);
    expect(groups.map((group) => group.observations.length)).toEqual([2, 2]);
  });

  it.each([
    { checked: -1 },
    { unknown: 2 },
    { kinds: { count: 0 } },
    { kinds: { count: 1, names: [{ kind: "" }] } },
    { gaps: [{ code: "" }] },
  ])("withholds invalid group fields without an all-clear: %j", (invalid) => {
    const result = project([
      tool("read", "diagnose", {
        resource: deployment,
        relatedIssues: [],
        resourceContext: {
          ...context,
          relatedFindings: [{ ...context.relatedFindings[0], ...invalid }],
        },
      }),
    ]);
    expect(groupsOf(result.groups, "issue")).toHaveLength(0);
    expect(
      result.limitations.some(
        (item) => item.source === "Related findings group",
      ),
    ).toBe(true);
    expect(
      groupsOf(result.groups, "receipt").filter(
        (group) =>
          group.latest.data.type === "receipt" &&
          group.latest.data.checked === "issues",
      ),
    ).toHaveLength(0);
  });

  it("distinguishes output truncation without requiring explanatory prose", () => {
    const result = project([
      tool("read", "get_resource", {
        resource: deployment,
        resourceContext: {
          ...context,
          relatedFindings: [{ ...context.relatedFindings[0], truncated: true }],
        },
      }),
    ]);
    expect(groupsOf(result.groups, "issue")).toHaveLength(1);
    expect(result.limitations).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          kind: "truncated",
          message: "Additional related findings were omitted.",
        }),
      ]),
    );
  });

  const relatedCases = [
    {
      label: "cert-manager issuance",
      anchor: {
        group: "cert-manager.io",
        kind: "Certificate",
        namespace: "shop",
        name: "tls",
      },
      subject: {
        group: "acme.cert-manager.io",
        kind: "Challenge",
        namespace: "shop",
        name: "http-01",
      },
    },
    {
      label: "External Secrets cluster store",
      anchor: {
        group: "external-secrets.io",
        kind: "ExternalSecret",
        namespace: "shop",
        name: "credentials",
      },
      subject: {
        group: "external-secrets.io",
        kind: "ClusterSecretStore",
        namespace: "",
        name: "shared",
      },
    },
    {
      label: "Crossplane composition",
      anchor: {
        group: "platform.example.org",
        kind: "Database",
        namespace: "",
        name: "orders",
      },
      subject: {
        group: "aws.example.org",
        kind: "Bucket",
        namespace: "shop",
        name: "storage",
      },
    },
    {
      label: "CloudNativePG backup",
      anchor: {
        group: "postgresql.cnpg.io",
        kind: "Cluster",
        namespace: "shop",
        name: "orders",
      },
      subject: {
        group: "postgresql.cnpg.io",
        kind: "Backup",
        namespace: "shop",
        name: "nightly",
      },
    },
    {
      label: "Flux dependency",
      anchor: {
        group: "kustomize.toolkit.fluxcd.io",
        kind: "Kustomization",
        namespace: "shop",
        name: "frontend",
      },
      subject: {
        group: "kustomize.toolkit.fluxcd.io",
        kind: "Kustomization",
        namespace: "infra",
        name: "database",
      },
    },
    {
      label: "Argo local managed resource",
      anchor: {
        group: "argoproj.io",
        kind: "Application",
        namespace: "argocd",
        name: "store",
      },
      subject: {
        group: "apps",
        kind: "Deployment",
        namespace: "shop",
        name: "worker",
      },
    },
  ];

  it.each(relatedCases)(
    "preserves subject identity and partial coverage for $label",
    ({ anchor, subject }) => {
      const findings = ["NotReady", "SyncFailed"].map((reason) => ({
        ...criticalIssue,
        ...subject,
        id: `${subject.group}:${subject.kind}:${subject.namespace}:${subject.name}:${reason}`,
        reason,
      }));
      const result = project([
        tool("read", "get_resource", {
          resource: deployment,
          resourceContext: {
            ...context,
            relatedFindings: [
              {
                kinds: {
                  count: 1,
                  names: [{ kind: subject.kind, group: subject.group }],
                },
                associatedVia: anchor,
                checked: 1,
                unknown: 0,
                findings,
                gaps: [{ code: "user_forbidden", action: "request_access" }],
              },
            ],
          },
        }),
      ]);
      const issues = groupsOf(result.groups, "issue");
      expect(issues).toHaveLength(2);
      for (const group of issues) {
        const { namespace, ...identity } = subject;
        const ref = investigationEvidenceSubjectRef(group.latest.data);
        expect(ref).toMatchObject(identity);
        expect(ref?.namespace).toBe(namespace || undefined);
        expect(group.latest.data).toMatchObject({ associatedVia: anchor });
      }
      expect(groupsOf(result.groups, "resource")[0].latest).toMatchObject({
        tier: "context",
        tone: "neutral",
        summary: "1/1 replicas ready",
      });
      expect(result.limitations.some((item) => item.kind === "unknown")).toBe(
        true,
      );
    },
  );

  it.each(relatedCases)(
    "does not retire $label findings from an empty parent verification",
    ({ anchor, subject }) => {
      const finding = {
        ...criticalIssue,
        ...subject,
        id: `${subject.group}:${subject.kind}:${subject.namespace}:${subject.name}:NotReady`,
        reason: "NotReady",
      };
      const relatedFindings = {
        kinds: {
          count: 1,
          names: [{ kind: subject.kind, group: subject.group }],
        },
        associatedVia: anchor,
        checked: 1,
        unknown: 0,
        findings: [finding],
        gaps: [],
      };
      const result = projectInvestigationEvidence(
        [
          {
            timeline: [
              tool("read", "diagnose", {
                resource: deployment,
                resourceContext: {
                  ...context,
                  relatedFindings: [relatedFindings],
                },
              }),
            ],
          },
          {
            verify: true,
            status: "done",
            timeline: [
              tool("verify", "diagnose", {
                resource: deployment,
                relatedIssues: [],
                resourceContext: {
                  ...context,
                  relatedFindings: [
                    {
                      ...relatedFindings,
                      findings: [],
                    },
                  ],
                },
              }),
            ],
          },
        ],
        target,
      );
      expect(
        groupsOf(result.groups, "issue").find(
          (group) => group.identity === `issue:${finding.id}`,
        )?.historical,
      ).toBe(false);
    },
  );

  it.each(["diagnose", "get_resource"])(
    "keeps connector identity and citation separate from healthy parent in %s",
    (name) => {
      const ref = evidenceRef("a", "b");
      const result = project([
        tool(
          "read",
          name,
          { resource: deployment, resourceContext: context },
          { evidenceRef: ref },
        ),
      ]);
      const finding = groupsOf(result.groups, "issue")[0];
      expect(finding.latest).toMatchObject({
        tier: "supporting",
        relevance: "producer-related",
        source: { evidenceRef: ref },
        data: { issue: connector, associatedVia: source },
      });
      expect(
        investigationEvidenceSubjectRef(finding.latest.data),
      ).toMatchObject({
        group: "kafka.strimzi.io",
        kind: "KafkaConnector",
        namespace: "shop",
        name: "sink",
      });
      expect(groupsOf(result.groups, "resource")[0].latest).toMatchObject({
        tier: "context",
        tone: "neutral",
        summary: "1/1 replicas ready",
      });
      expect(result.limitations).toHaveLength(0);
      expect(
        groupsOf(result.groups, "receipt").filter(
          (group) =>
            group.latest.data.type === "receipt" &&
            group.latest.data.checked === "issues",
        ),
      ).toHaveLength(0);
    },
  );

  it("does not clear captured connector proof when a later parent diagnosis lacks it", () => {
    const result = projectInvestigationEvidence(
      [
        {
          timeline: [
            tool("read", "diagnose", {
              resource: deployment,
              resourceContext: context,
            }),
          ],
        },
        {
          verify: true,
          status: "done",
          timeline: [
            tool("verify", "diagnose", {
              resource: deployment,
              relatedIssues: [],
            }),
          ],
        },
      ],
      target,
    );
    expect(groupsOf(result.groups, "issue")[0].historical).toBe(false);
  });

  it("shows incomplete inspection without emitting a healthy receipt", () => {
    const result = project([
      tool("read", "diagnose", {
        resource: deployment,
        resourceContext: {
          tier: "basic",
          relatedFindings: [
            {
              kinds: {
                count: 1,
                names: [{ kind: "KafkaConnector", group: "kafka.strimzi.io" }],
              },
              associatedVia: source,
              gaps: [{ code: "list_too_large", action: "change_request" }],
              checked: 0,
              unknown: 0,
            },
          ],
        },
      }),
    ]);
    expect(groupsOf(result.groups, "issue")).toHaveLength(0);
    expect(
      groupsOf(result.groups, "receipt").filter(
        (group) =>
          group.latest.data.type === "receipt" &&
          group.latest.data.checked === "issues",
      ),
    ).toHaveLength(0);
    expect(result.limitations).toEqual(
      expect.arrayContaining([expect.objectContaining({ kind: "unknown" })]),
    );
  });

  it.each(["KafkaConnector", "KafkaConnect"])(
    "does not invent event or change reads for cached %s diagnosis",
    (kind) => {
      const result = project([
        tool("read", "diagnose", {
          resource: {
            apiVersion: "kafka.strimzi.io/v1",
            kind,
            metadata: { namespace: "shop", name: "connect" },
          },
          resourceContext: { tier: "basic" },
          coverage:
            "Cached operator evidence only; Pods, logs and live application endpoints were not collected.",
        }),
      ]);
      expect(groupsOf(result.groups, "receipt")).toHaveLength(0);
    },
  );

  it("still retires the parent's own repaired issue while retaining connector proof", () => {
    const result = projectInvestigationEvidence(
      [
        {
          timeline: [
            tool("read", "diagnose", {
              resource: deployment,
              relatedIssues: [criticalIssue],
              resourceContext: context,
            }),
          ],
        },
        {
          verify: true,
          status: "done",
          timeline: [
            tool("verify", "diagnose", {
              resource: deployment,
              relatedIssues: [],
              resourceContext: {
                ...context,
                relatedFindings: [
                  {
                    ...context.relatedFindings[0],
                    findings: [],
                  },
                ],
              },
            }),
          ],
        },
      ],
      target,
    );
    expect(
      groupsOf(result.groups, "issue").find(
        (group) => group.identity === `issue:${criticalIssue.id}`,
      )?.historical,
    ).toBe(true);
    expect(
      groupsOf(result.groups, "issue").find(
        (group) => group.identity === `issue:${connector.id}`,
      )?.historical,
    ).toBe(false);
  });

  it.each([true, false])(
    "retires a connector target only with explicit observed recovery (%s)",
    (recovered) => {
      const resource = {
        apiVersion: "kafka.strimzi.io/v1",
        kind: "KafkaConnector",
        metadata: { namespace: "shop", name: "sink" },
      };
      const result = projectInvestigationEvidence(
        [
          {
            timeline: [
              tool("read", "get_resource", {
                resource: deployment,
                resourceContext: context,
              }),
            ],
          },
          {
            verify: true,
            status: "done",
            timeline: [
              tool("verify", "diagnose", {
                resource,
                resourceContext: { tier: "basic" },
                operatorSnapshot: {
                  state: recovered ? "recovered" : "unknown",
                  generationCurrent: recovered,
                  readiness: recovered ? "ready" : "unknown",
                  reason: recovered ? undefined : "stale",
                },
              }),
            ],
          },
        ],
        {
          group: "kafka.strimzi.io",
          kind: "KafkaConnector",
          namespace: "shop",
          name: "sink",
        },
      );
      expect(groupsOf(result.groups, "issue")[0].historical).toBe(recovered);
    },
  );

  it.each(["shop", "other"])(
    "uses connector-scoped recovery in a parent investigation (%s)",
    (namespace) => {
      const resource = {
        apiVersion: "kafka.strimzi.io/v1",
        kind: "KafkaConnector",
        metadata: { namespace, name: "sink" },
      };
      const result = projectInvestigationEvidence(
        [
          {
            timeline: [
              tool("read", "diagnose", {
                resource: deployment,
                resourceContext: context,
              }),
            ],
          },
          {
            verify: true,
            status: "done",
            timeline: [
              tool("verify", "diagnose", {
                resource,
                operatorSnapshot: {
                  state: "recovered",
                  generationCurrent: true,
                  readiness: "ready",
                },
                resourceContext: { tier: "basic" },
              }),
            ],
          },
        ],
        target,
      );
      expect(groupsOf(result.groups, "issue")[0].historical).toBe(
        namespace === "shop",
      );
    },
  );

  it("shows unverifiable operator status as a limitation", () => {
    const result = project([
      tool("read", "diagnose", {
        resource: {
          apiVersion: "kafka.strimzi.io/v1",
          kind: "KafkaConnector",
          metadata: { namespace: "shop", name: "sink" },
        },
        resourceContext: { tier: "basic" },
        operatorSnapshot: {
          state: "unknown",
          generationCurrent: false,
          readiness: "unknown",
          reason: "stale",
        },
      }),
    ]);
    expect(result.limitations).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          source: "Operator snapshot",
          message: expect.stringContaining("stale"),
        }),
      ]),
    );
  });

  it("shows KafkaConnect's own NotReady condition separately from connector findings", () => {
    const result = project([
      tool("read", "diagnose", {
        resource: {
          apiVersion: "kafka.strimzi.io/v1",
          kind: "KafkaConnect",
          metadata: { namespace: "shop", name: "connect" },
        },
        resourceContext: {
          tier: "basic",
          statusSummary: {
            phase: "NotReady",
            conditions: [{ type: "Ready", status: "False" }],
          },
        },
        operatorSnapshot: {
          state: "observed",
          generationCurrent: true,
          readiness: "not_ready",
        },
      }),
    ]);
    expect(groupsOf(result.groups, "resource")[0].latest).toMatchObject({
      tone: "warning",
      summary: "NotReady",
    });
    expect(groupsOf(result.groups, "issue")).toHaveLength(0);
  });

  it("rejects malformed findings without projecting partial proof", () => {
    const result = project([
      tool("read", "get_resource", {
        resource: deployment,
        resourceContext: {
          ...context,
          relatedFindings: [
            {
              ...context.relatedFindings[0],
              findings: [connector, { id: "bad" }],
            },
          ],
        },
      }),
    ]);
    expect(groupsOf(result.groups, "issue")).toHaveLength(0);
    expect(
      result.limitations.some(
        (item) => item.source === "Related findings group",
      ),
    ).toBe(true);
  });
});
