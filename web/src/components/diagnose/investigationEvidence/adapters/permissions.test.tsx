import { describe, expect, it } from "vitest";
import {
  investigationEvidenceSubjectRef,
  projectInvestigationEvidence,
} from "../index";
import { deployment, groupsOf, project, tool } from "../evidenceFixtures";
import { renderToStaticMarkup } from "react-dom/server";
import { PermissionsBody } from "../bodies/platform";

const deploymentWithServiceAccount = {
  ...deployment,
  spec: { template: { spec: { serviceAccountName: "api-sa" } } },
};

const permissionsArgs = JSON.stringify({
  kind: "ServiceAccount",
  namespace: "shop",
  name: "api-sa",
  verb: "get",
  resource: "secrets",
});

describe("subject permissions adapter", () => {
  it("does not turn an unevaluated access check into a denial", () => {
    // A webhook authorizer returning partial data has established neither
    // answer. Reading it as "cannot" sends an operator after an RBAC grant
    // that may already exist.
    const projection = project([
      tool("diagnose", "diagnose", {
        resource: deploymentWithServiceAccount,
        resourceContext: { tier: "basic" },
      }),
      tool(
        "perm",
        "get_subject_permissions",
        {
          subject: {
            kind: "ServiceAccount",
            namespace: "shop",
            name: "api-sa",
          },
          accessCheck: {
            verb: "get",
            resource: "secrets",
            namespace: "shop",
            allowed: false,
            denied: false,
            reason: "",
            evaluationError: "webhook authorizer returned partial data",
          },
        },
        { summary: permissionsArgs },
      ),
    ]);
    const [group] = groupsOf(projection.groups, "permissions");
    expect(group.latest.title).toContain("Could not check whether");
    expect(group.latest.title).not.toContain("cannot get");
    expect(group.latest.summary).toContain(
      "webhook authorizer returned partial data",
    );
    expect(group.latest.summary).not.toContain("No RBAC rule allows it");
    // The gap still reaches the coverage strip as well.
    expect(projection.limitations).toContainEqual(
      expect.objectContaining({ source: "Permissions", kind: "error" }),
    );
  });

  it("keeps a decided verdict that arrived with an evaluation error", () => {
    // Kubernetes returns the error alongside a real answer too — one webhook
    // failing while another allows. That answer is still the answer.
    const projection = project([
      tool("diagnose", "diagnose", {
        resource: deploymentWithServiceAccount,
        resourceContext: { tier: "basic" },
      }),
      tool(
        "perm",
        "get_subject_permissions",
        {
          subject: {
            kind: "ServiceAccount",
            namespace: "shop",
            name: "api-sa",
          },
          accessCheck: {
            verb: "get",
            resource: "secrets",
            namespace: "shop",
            allowed: true,
            denied: false,
            reason: "allowed by one authorizer",
            evaluationError: "a second webhook did not answer",
          },
        },
        { summary: permissionsArgs },
      ),
    ]);
    const [group] = groupsOf(projection.groups, "permissions");
    expect(group.latest.title).toContain("can get");
    expect(group.latest.title).not.toContain("Could not check");
    expect(group.latest.tone).toBe("info");
    // And the error is still reported, just not as the verdict.
    expect(projection.limitations).toContainEqual(
      expect.objectContaining({ source: "Permissions", kind: "error" }),
    );
  });

  it("binds an access check to the target's ServiceAccount seen in the same turn", () => {
    const check = {
      subject: { kind: "ServiceAccount", namespace: "shop", name: "api-sa" },
      accessCheck: {
        verb: "get",
        resource: "secrets",
        namespace: "shop",
        allowed: false,
        reason: "",
      },
    };
    const projection = project([
      tool("diagnose", "diagnose", {
        resource: deploymentWithServiceAccount,
        resourceContext: { tier: "basic" },
      }),
      tool("perm", "get_subject_permissions", check, {
        summary: permissionsArgs,
      }),
      tool(
        "perm-other",
        "get_subject_permissions",
        {
          subject: { kind: "ServiceAccount", namespace: "shop", name: "other" },
          accessCheck: {
            verb: "list",
            group: "apps",
            resource: "deployments",
            subresource: "scale",
            namespace: "",
            allowed: true,
            reason: 'RBAC: allowed by ClusterRoleBinding "admin"',
          },
        },
        {
          summary: JSON.stringify({
            kind: "ServiceAccount",
            namespace: "shop",
            name: "other",
          }),
        },
      ),
    ]);
    const permissions = groupsOf(projection.groups, "permissions");
    expect(permissions.map((group) => group.latest.relevance)).toEqual([
      "target",
      "broader",
    ]);
    expect(permissions[0].latest).toMatchObject({
      tier: "supporting",
      tone: "warning",
      title: "Service Account shop/api-sa cannot get secrets",
      summary: "No RBAC rule allows it · namespace shop",
    });
    expect(permissions[0].latest.data).toMatchObject({
      type: "permissions",
      accessCheck: { allowed: false, denied: false },
    });
    expect(permissions[1].latest).toMatchObject({
      tier: "context",
      tone: "info",
      title: "Service Account shop/other can list deployments/scale.apps",
      summary: 'RBAC: allowed by ClusterRoleBinding "admin" · cluster-wide',
    });
    expect(investigationEvidenceSubjectRef(permissions[0].latest.data)).toEqual(
      { kind: "ServiceAccount", namespace: "shop", name: "api-sa" },
    );
  });

  it("uses the producer's serviceAccount reference and the default account when the template omits one", () => {
    const withContext = project([
      tool("diagnose", "diagnose", {
        resource: deployment,
        resourceContext: {
          tier: "diagnostic",
          uses: {
            serviceAccount: {
              kind: "ServiceAccount",
              namespace: "shop",
              name: "ctx-sa",
            },
          },
        },
      }),
      tool(
        "perm",
        "get_subject_permissions",
        {
          subject: {
            kind: "ServiceAccount",
            namespace: "shop",
            name: "ctx-sa",
          },
          bindings: [],
          flatRules: [],
        },
        { summary: permissionsArgs },
      ),
    ]);
    expect(
      groupsOf(withContext.groups, "permissions")[0].latest.relevance,
    ).toBe("target");

    const withDefault = project([
      tool("resource", "get_resource", {
        ...deployment,
        spec: { template: { spec: {} } },
      }),
      tool(
        "perm",
        "get_subject_permissions",
        {
          subject: {
            kind: "ServiceAccount",
            namespace: "shop",
            name: "default",
          },
          bindings: [],
          flatRules: [],
        },
        { summary: permissionsArgs },
      ),
    ]);
    expect(
      groupsOf(withDefault.groups, "permissions")[0].latest.relevance,
    ).toBe("target");

    const priorTurn = project(
      [
        tool("diagnose", "diagnose", {
          resource: deploymentWithServiceAccount,
          resourceContext: { tier: "basic" },
        }),
      ],
      [
        tool(
          "perm",
          "get_subject_permissions",
          {
            subject: {
              kind: "ServiceAccount",
              namespace: "shop",
              name: "api-sa",
            },
            bindings: [],
            flatRules: [],
          },
          { summary: permissionsArgs },
        ),
      ],
    );
    expect(groupsOf(priorTurn.groups, "permissions")[0].latest.relevance).toBe(
      "broader",
    );

    // The newest captured read of the target decides which account it runs as.
    const rotated = project([
      tool("resource-old", "get_resource", deploymentWithServiceAccount),
      tool("resource-new", "get_resource", {
        ...deployment,
        spec: { template: { spec: { serviceAccountName: "api-sa-v2" } } },
      }),
      tool(
        "perm-old",
        "get_subject_permissions",
        {
          subject: {
            kind: "ServiceAccount",
            namespace: "shop",
            name: "api-sa",
          },
          bindings: [],
          flatRules: [],
        },
        { summary: permissionsArgs },
      ),
      tool(
        "perm-new",
        "get_subject_permissions",
        {
          subject: {
            kind: "ServiceAccount",
            namespace: "shop",
            name: "api-sa-v2",
          },
          bindings: [],
          flatRules: [],
        },
        { summary: permissionsArgs },
      ),
    ]);
    expect(
      groupsOf(rotated.groups, "permissions").map(
        (group) => group.latest.relevance,
      ),
    ).toEqual(["broader", "target"]);
  });

  it("reads a PodSpec only from kinds that carry one", () => {
    const check = (name: string) => ({
      subject: { kind: "ServiceAccount", namespace: "shop", name },
      accessCheck: {
        verb: "get",
        resource: "secrets",
        namespace: "shop",
        allowed: false,
      },
    });
    const applicationSet = project([
      tool("resource", "get_resource", {
        apiVersion: "argoproj.io/v1alpha1",
        kind: "ApplicationSet",
        metadata: { namespace: "shop", name: "api" },
        spec: { template: { spec: { project: "default" } } },
      }),
      tool("perm", "get_subject_permissions", check("default"), {
        summary: permissionsArgs,
      }),
    ]);
    expect(
      groupsOf(applicationSet.groups, "permissions")[0].latest.relevance,
    ).toBe("broader");

    const cronJob = projectInvestigationEvidence(
      [
        {
          timeline: [
            tool("resource", "get_resource", {
              apiVersion: "batch/v1",
              kind: "CronJob",
              metadata: { namespace: "shop", name: "api" },
              spec: {
                jobTemplate: {
                  spec: {
                    template: { spec: { serviceAccountName: "cron-sa" } },
                  },
                },
              },
            }),
            tool("perm", "get_subject_permissions", check("cron-sa"), {
              summary: permissionsArgs,
            }),
          ],
        },
      ],
      { kind: "CronJob", group: "batch", namespace: "shop", name: "api" },
    );
    expect(groupsOf(cronJob.groups, "permissions")[0].latest.relevance).toBe(
      "target",
    );
  });

  it("keeps a denial for an unrelated principal out of the lead evidence", () => {
    const projection = project([
      tool("diagnose", "diagnose", {
        resource: deploymentWithServiceAccount,
        resourceContext: { tier: "basic" },
      }),
      tool(
        "perm-cross",
        "get_subject_permissions",
        {
          subject: {
            kind: "ServiceAccount",
            namespace: "unrelated",
            name: "other",
          },
          accessCheck: {
            verb: "get",
            resource: "secrets",
            namespace: "unrelated",
            allowed: false,
          },
        },
        {
          summary: JSON.stringify({
            kind: "ServiceAccount",
            namespace: "unrelated",
            name: "other",
          }),
        },
      ),
    ]);
    const denial = groupsOf(projection.groups, "permissions")[0].latest;
    expect(denial.relevance).toBe("broader");
    expect(denial.tier).toBe("context");
    expect(denial.tone).toBe("warning");
  });

  it("renders the subject response as counts and keeps every coverage caveat", () => {
    const projection = project([
      tool(
        "perm",
        "get_subject_permissions",
        {
          subject: {
            kind: "ServiceAccount",
            namespace: "shop",
            name: "api-sa",
          },
          bindings: [
            {
              bindingKind: "RoleBinding",
              bindingNamespace: "shop",
              bindingName: "api-reader",
              roleKind: "Role",
              roleNamespace: "shop",
              roleName: "reader",
              rulesCount: 3,
            },
            {
              bindingKind: "ClusterRoleBinding",
              bindingName: "system:basic-user",
              roleKind: "ClusterRole",
              roleName: "system:basic-user",
              rulesCount: 1,
              inheritedFromGroup: "system:authenticated",
            },
          ],
          flatRules: [
            { verbs: ["get"], resources: ["pods"] },
            { verbs: ["list"], resources: ["pods"] },
          ],
          truncated: true,
          usedByPods: ["shop/api-68c7b766dc-fmphn"],
          podsTotal: 3,
          narrowHint:
            "rule list truncated — the subject has more rules than shown; do not treat this list as the subject's complete permissions",
        },
        { summary: permissionsArgs },
      ),
      tool(
        "perm-missing",
        "get_subject_permissions",
        {
          subject: { kind: "ServiceAccount", namespace: "shop", name: "ghost" },
          bindings: [],
          flatRules: [],
          subjectWarning:
            'no ServiceAccount "ghost" exists in namespace "shop" — this empty result reflects a subject that was never found, not an account without permissions. Check the name and namespace.',
        },
        { summary: permissionsArgs },
      ),
    ]);
    const permissions = groupsOf(projection.groups, "permissions");
    expect(permissions[0].latest).toMatchObject({
      tier: "context",
      tone: "info",
      relevance: "broader",
      title: "Permissions of Service Account shop/api-sa",
      summary: "2 bindings · 2+ effective rules",
    });
    expect(permissions[0].latest.data).toMatchObject({
      type: "permissions",
      flatRulesCount: 2,
      truncated: true,
      usedByPods: ["shop/api-68c7b766dc-fmphn"],
      podsTotal: 3,
    });
    expect(permissions[1].latest.summary).toBe(
      "0 bindings · 0 effective rules",
    );
    expect(
      projection.limitations.map((limitation) => [
        limitation.source,
        limitation.kind,
      ]),
    ).toEqual([
      ["Permissions", "truncated"],
      ["Permissions", "truncated"],
      ["Permissions", "unknown"],
    ]);
    expect(projection.limitations[0].message).toContain("rule list truncated");
    expect(projection.limitations[1].message).toBe(
      "Only 1 of 3 pods running as this subject were listed.",
    );
  });

  it("rejects malformed permission payloads", () => {
    const projection = project([
      tool(
        "perm-bad",
        "get_subject_permissions",
        { subject: { kind: "ServiceAccount" }, bindings: [], flatRules: [] },
        { summary: permissionsArgs },
      ),
      tool(
        "perm-bad-check",
        "get_subject_permissions",
        {
          subject: { kind: "ServiceAccount", namespace: "shop", name: "x" },
          accessCheck: { verb: "get", resource: "secrets", namespace: "shop" },
        },
        { summary: permissionsArgs },
      ),
      tool(
        "perm-bad-binding",
        "get_subject_permissions",
        {
          subject: { kind: "ServiceAccount", namespace: "shop", name: "x" },
          bindings: [{ bindingKind: "RoleBinding" }],
          flatRules: [],
        },
        { summary: permissionsArgs },
      ),
      tool(
        "perm-bad-rule",
        "get_subject_permissions",
        {
          subject: { kind: "ServiceAccount", namespace: "shop", name: "x" },
          bindings: [],
          flatRules: [{ resources: ["pods"] }],
        },
        { summary: permissionsArgs },
      ),
    ]);
    expect(projection.groups).toHaveLength(0);
    expect(
      projection.limitations.map((limitation) => limitation.source),
    ).toEqual(["Permissions", "Access check"]);
    expect(projection.limitations[0].sources).toHaveLength(3);
  });
});

describe("subject permissions card detail", () => {
  it("keeps the effective rules and renders them", () => {
    const projection = project([
      tool("diagnose", "diagnose", {
        resource: deploymentWithServiceAccount,
        resourceContext: { tier: "basic" },
      }),
      tool(
        "perm",
        "get_subject_permissions",
        {
          subject: {
            kind: "ServiceAccount",
            namespace: "shop",
            name: "api-sa",
          },
          bindings: [],
          flatRules: [
            {
              verbs: ["get", "list"],
              apiGroups: [""],
              resources: ["pods", "pods/log"],
            },
            {
              verbs: ["get"],
              apiGroups: [""],
              resources: ["secrets"],
              resourceNames: ["db-creds"],
            },
          ],
          truncated: false,
        },
        { summary: permissionsArgs },
      ),
    ]);
    const [group] = groupsOf(projection.groups, "permissions");
    const data = group.latest.data;
    if (data.type !== "permissions")
      throw new Error("expected a permissions card");
    expect(data.rules?.length).toBe(2);
    const html = renderToStaticMarkup(<PermissionsBody data={data} />);
    expect(html).toContain("Effective rules");
    expect(html).toContain("get, list");
    expect(html).toContain("pods, pods/log");
    expect(html).toContain("[db-creds]");
  });
});
