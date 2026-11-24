import { useState, type ReactNode } from "react";
import { ExternalLink } from "lucide-react";
import type { RenderedFieldChange, RenderedResourceChange, RenderedSyncDiff } from "../../api/client";

const deploymentAnnotations = new Set(["build.run.url", "deployment-id", "deployment-timestamp"]);
const rolloutWorkloads = new Set(["apps/Deployment", "apps/StatefulSet", "apps/DaemonSet", "argoproj.io/Rollout"]);
const workloadTemplates = new Set([...rolloutWorkloads, "apps/ReplicaSet", "batch/Job", "/ReplicationController"]);

function podTemplate(resource: RenderedResourceChange): string[] | undefined {
  const type = `${resource.group ?? ""}/${resource.kind}`;
  if (type === "/Pod") return [];
  if (type === "batch/CronJob") return ["spec", "jobTemplate", "spec", "template"];
  if (workloadTemplates.has(type)) return ["spec", "template"];
}

function matchesPath(path: string[], expected: string[]) {
  return path.length === expected.length && expected.every((part, i) => part === path[i]);
}

function isResourceMetadata(path: string[], section: "labels" | "annotations", resource: RenderedResourceChange, template: string[] | undefined): boolean {
  if (path.at(-2) !== section || path.at(-3) !== "metadata") return false;
  const prefix = path.slice(0, -3);
  return prefix.length === 0 || (template !== undefined && matchesPath(prefix, template)) ||
    (resource.group === "batch" && resource.kind === "CronJob" && matchesPath(prefix, ["spec", "jobTemplate"]));
}

function isContainerImage(path: string[], template: string[] | undefined): boolean {
  return template !== undefined && path.at(-1) === "image" && /^\[name=.+\]$/.test(path.at(-2) ?? "") &&
    ["containers", "initContainers", "ephemeralContainers"].includes(path.at(-3) ?? "") &&
    matchesPath(path.slice(0, -3), [...template, "spec"]);
}

function fieldPath(path: string[]) {
  return path.map((part, i) => {
    if (part === "[order]" || /^\[(name|mountPath)=.+\]$/.test(part)) return part;
    if (/^\d+$/.test(part)) return `[${part}]`;
    if (!/^[A-Za-z_][A-Za-z_0-9]*$/.test(part)) return `[${JSON.stringify(part)}]`;
    return i ? `.${part}` : part;
  }).join("");
}

function buildURL(value: unknown) {
  if (typeof value !== "string") return;
  try {
    const url = new URL(value);
    if (url.protocol === "https:" && !url.username && !url.password) return url.href;
  } catch { /* Annotation values can be arbitrary strings. */ }
}

function FieldValue({ value, other }: { value: unknown; other: unknown }) {
  if (value === undefined) return <span className="italic text-theme-text-tertiary">Absent</span>;
  // Quote strings on a type change so "3" → 3 and "null" → null remain distinguishable.
  const plain = typeof value === "string" && value !== "" && (other === undefined || typeof other === "string");
  const display = plain ? value : JSON.stringify(value, null, 2);
  return <code className="whitespace-pre-wrap break-all">{display}</code>;
}

function ChangeValues({ fields, images = false }: { fields: RenderedFieldChange[]; images?: boolean }) {
  return <div className="overflow-x-auto rounded border border-theme-border bg-theme-surface">
    <table className="w-full table-fixed text-left text-xs">
      <thead className="border-b border-theme-border text-theme-text-secondary">
        <tr>{[images ? "Container image" : "Field", "Previous sync", "Selected sync"].map(label =>
          <th key={label} scope="col" className="px-3 py-1.5 font-medium">{label}</th>)}</tr>
      </thead>
      <tbody>{fields.map(field => <tr key={JSON.stringify(field.path)} className="border-b border-theme-border align-top last:border-0">
        <th scope="row" className="break-all px-3 py-2 text-left font-mono font-normal" title={fieldPath(field.path)}>
          {images ? <>
            {field.path.at(-2)!.slice("[name=".length, -1)}
            <span className="block font-sans text-theme-text-secondary">
              {field.path.at(-3) === "initContainers" && "Init container "}
              {field.path.at(-3) === "ephemeralContainers" && "Ephemeral container "}
              {field.before === undefined && "Image added"}{field.after === undefined && "Image removed"}
            </span>
          </> : fieldPath(field.path)}
        </th>
        <td className="px-3 py-2 text-theme-text-secondary"><FieldValue value={field.before} other={field.after} /></td>
        <td className="px-3 py-2 text-theme-text-primary"><FieldValue value={field.after} other={field.before} /></td>
      </tr>)}</tbody>
    </table>
  </div>;
}

function Disclosure({ label, children }: { label: ReactNode; children: ReactNode }) {
  const [open, setOpen] = useState(false);
  return <details onToggle={event => setOpen(event.currentTarget.open)}>
    <summary className="cursor-pointer py-1 text-xs font-medium text-theme-text-secondary hover:text-theme-text-primary">{label}</summary>
    {open && <div className="mt-1 space-y-2">{children}</div>}
  </details>;
}

function resourceName(resource: RenderedResourceChange) {
  return [resource.namespace, resource.name].filter(Boolean).join("/");
}

function ResourceTitle({ resource }: { resource: RenderedResourceChange }) {
  return <p className="flex flex-wrap items-baseline gap-x-2">
    <span className="break-all font-medium text-theme-text-primary">{resource.kind} <span className="font-mono">{resourceName(resource)}</span></span>
    {resource.group && <span className="text-xs text-theme-text-tertiary">{resource.group}</span>}
    {resource.operation !== "modified" && <span className="text-xs font-medium">{resource.operation === "added" ? "Added" : "Removed"}</span>}
  </p>;
}

export function RenderedChanges({ diff }: { diff: RenderedSyncDiff }) {
  type VersionChange = { resource: RenderedResourceChange; field: RenderedFieldChange };
  const versions = new Map<string, VersionChange[]>();
  for (const resource of diff.changes) {
    if (resource.operation !== "modified") continue;
    const template = podTemplate(resource);
    for (const field of resource.fields) {
      if (field.path.at(-1) !== "app.kubernetes.io/version" || typeof field.before !== "string" || typeof field.after !== "string" ||
          !isResourceMetadata(field.path, "labels", resource, template)) continue;
      const key = JSON.stringify([field.before, field.after]);
      const entry = { resource, field };
      const changes = versions.get(key);
      if (changes) changes.push(entry);
      else versions.set(key, [entry]);
    }
  }
  const repeatedVersions = [...versions.values()].filter(entries => entries.length > 1);
  const groupedFields = new Set(repeatedVersions.flatMap(entries => entries.map(entry => entry.field)));
  const groups = diff.changes.map(resource => {
    const template = podTemplate(resource);
    const metadata: RenderedFieldChange[] = [], images: RenderedFieldChange[] = [], fields: RenderedFieldChange[] = [];
    for (const field of resource.fields) {
      if (groupedFields.has(field)) continue;
      if (deploymentAnnotations.has(field.path.at(-1) ?? "") && isResourceMetadata(field.path, "annotations", resource, template)) metadata.push(field);
      else if (isContainerImage(field.path, template)) images.push(field);
      else fields.push(field);
    }
    // Show spec/config changes above repeated labels and resource identity fields.
    fields.sort((a, b) => Number(["metadata", "apiVersion", "kind"].includes(a.path[0])) -
      Number(["metadata", "apiVersion", "kind"].includes(b.path[0])));
    return { resource, images, fields, metadata, key: JSON.stringify([resource.group, resource.kind, resource.namespace, resource.name]) };
  });
  const substantive = groups.filter(g => g.fields.length || g.images.length).sort((a, b) => Number(!!b.images.length) - Number(!!a.images.length));
  const metadataOnlyOrEmpty = substantive.length === 0 && repeatedVersions.length === 0;
  const metadataGroups = groups.filter(g => g.metadata.length);
  const metaCount = metadataGroups.reduce((n, g) => n + g.metadata.length, 0);
  const rolloutTemplateChanged = metadataGroups.some(({ resource, metadata }) =>
    rolloutWorkloads.has(`${resource.group}/${resource.kind}`) &&
    metadata.some(field => matchesPath(field.path.slice(0, -3), ["spec", "template"])));
  const builds = new Set<string>();
  for (const group of metadataGroups) for (const field of group.metadata) {
    if (field.path.at(-1) === "build.run.url") {
      const url = buildURL(field.after);
      if (url) builds.add(url);
    }
  }
  return <section aria-label="Rendered changes" className="space-y-3 rounded-lg border border-theme-border bg-theme-surface p-3">
    <div>
      <h3 className="font-semibold text-theme-text-primary">Rendered changes</h3>
      <p className="text-xs text-theme-text-secondary">Desired resources rendered by Argo CD at both Git revisions.</p>
    </div>
    {diff.partial && <p className="text-xs text-amber-700 dark:text-amber-300">Partial comparison. Some resources could not be included.</p>}
    {diff.secretsMasked && <p className="text-xs text-theme-text-secondary">Secret values are masked. Changes to those values may be unavailable.</p>}
    {metadataOnlyOrEmpty && <p>{metaCount
      ? (diff.partial || diff.secretsMasked ? "The visible changes are deployment metadata." : "Only deployment metadata changed.")
      : (diff.partial || diff.secretsMasked ? "No visible field changes." : "No rendered resource changes.")}</p>}
    {metadataOnlyOrEmpty && rolloutTemplateChanged && <p className="text-xs text-theme-text-secondary">
      Pod template annotations changed and can trigger a rollout.
    </p>}
    {substantive.map(({ resource, fields, images, key }, index) => {
      const previewCount = index < 4 ? Math.min(fields.length, 2) : 0;
      const remaining = fields.length - previewCount;
      return <div key={key} className="space-y-1.5">
        <ResourceTitle resource={resource} />
        {!!images.length && <ChangeValues fields={images} images />}
        {previewCount > 0 && <ChangeValues fields={fields.slice(0, previewCount)} />}
        {remaining > 0 && <Disclosure label={`${remaining} ${previewCount || images.length ? "more " : ""}field ${remaining === 1 ? "change" : "changes"}`}>
          <ChangeValues fields={fields.slice(previewCount)} />
        </Disclosure>}
      </div>;
    })}
    {repeatedVersions.map(entries => {
      const { before, after } = entries[0].field;
      const resources = new Set(entries.map(entry => entry.resource)).size;
      return <Disclosure key={JSON.stringify([before, after])} label={<>
        Version label <FieldValue value={before} other={after} /> → <FieldValue value={after} other={before} /> on {resources} {resources === 1 ? "resource" : "resources"}
      </>}>
        <ul className="space-y-1">
          {entries.map(({ resource, field }) => <li key={JSON.stringify([resource.kind, resource.group, resource.namespace, resource.name, field.path])} className="text-xs">
            <span className="font-medium">{resource.kind} {resourceName(resource)}{resource.group && ` (${resource.group})`}</span>
            {" · "}<code className="break-all">{fieldPath(field.path)}</code>
          </li>)}
        </ul>
      </Disclosure>;
    })}
    {!!metaCount && <div className="border-t border-theme-border pt-2">
      <Disclosure label={`Deployment metadata (${metaCount} ${metaCount === 1 ? "change" : "changes"})`}>
        {metadataGroups.map(({ resource, metadata, key }) => <div className="space-y-1" key={key}>
          <ResourceTitle resource={resource} />
          <ChangeValues fields={metadata} />
        </div>)}
      </Disclosure>
      {[...builds].map(url => <a key={url} href={url} target="_blank" rel="noopener noreferrer" className="mr-3 inline-flex items-center gap-1 text-xs font-medium text-accent-text hover:underline">
        {builds.size === 1 ? "View build run" : url} <ExternalLink aria-hidden className="h-3 w-3" />
      </a>)}
    </div>}
  </section>;
}
