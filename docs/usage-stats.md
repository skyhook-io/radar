# Usage stats

Anonymous and strictly opt-in: usage stats show us which features get used, so
we know what to build next. They go straight to us, with no third-party
trackers, and never include names, contents, logs or the resources in your
clusters.

Settings > Privacy shows the exact report before anything is sent.

## When Radar asks

- **A local Radar** asks once, in a small card the first time it runs, and in
  the What's New dialog after an upgrade (it opens by itself only for releases
  with enough to show; otherwise from the nav rail). "No thanks" is final. If you close
  the question without answering, Radar asks again after three months.
- **A shared Radar** never asks; its operator decides. See
  [Shared Radar](#shared-radar).

You can change your answer any time in Settings > Privacy.

## What's sent

Once a day, and only if Radar was used:

- Radar's version, OS, architecture, install method, and whether it runs
  locally or in a cluster.
- Radar's setup: auth mode, timeline storage, whether MCP is on, whether
  Prometheus was found in the cluster or set by hand, cost source, and browser
  family.
- Yes or no: whether an AI agent can run investigations, and whether Radar can
  read Argo CD or Flux. Which traffic source is in use, from a fixed list.
- How often each view, action and MCP tool was used, how often the command
  palette and search were opened, which requests failed, and which screens
  crashed. Actions are named by Radar's own route, never by what they acted
  on, for example `POST /api/helm/releases/{namespace}/{name}/rollback`.
  Deletes and restarts also name the kind when it is built into Kubernetes,
  such as `pods`. Your own resource types are never named.
- How often connecting to a cluster failed, by the kind of failure, such as
  `auth`, `network` or `tls`.
- Sessions, and active time as a range.
- How many kubeconfig contexts you have, as a range, and for each cluster used:
  Kubernetes minor version, platform, node count as a range, and known
  integrations from a fixed list.

It answers three questions: which features get used, how teams running Radar
in a cluster use it compared with people running it locally, and which
integrations matter most.

## What isn't

No install ID or cluster ID. No timestamps of when you do anything, only daily
counts. No names of resources, namespaces, clusters, contexts or hosts. No
manifests, logs, events, metric values, URLs or anything you type. No API
groups Radar doesn't recognize, since they may be your own.

## Where it goes

Straight to `usage.radarhq.io`. Radar has no third-party trackers. The
receiver rejects anything that isn't a report like the one above, and stores
each report in our product analytics (PostHog) under a new random ID, without
your IP address or its location. It is not used for sales.

## Turning it off

| Where | How |
|-------|-----|
| Settings > Privacy | Turn the switch off. This also deletes the pending report, `~/.radar/usage-report.json`. |
| Helm | `usageReporting.enabled: false` (or `true`) decides for everyone using that Radar. |

## Shared Radar

A shared Radar (in a cluster, behind sign-in, or on an address others can
reach) reports how the team uses it, so its configuration decides for
everyone and nobody is asked: `usageReporting.enabled: true` in the Helm
chart. Settings > Privacy shows the current state read-only. In a cluster,
Radar also sends what it has when its pod stops, since the pending report
doesn't outlive the pod.
