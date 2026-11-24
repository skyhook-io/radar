# Replay a rightsizing scan

Compare the shipped recommendation with a candidate that takes larger reduction steps as more of the week's demand is observed. This developer command runs offline and writes one JSON object per container resource. The Radar server, Hub, and their recommendation totals keep using the shipped policy.

From the repository root:

```sh
go run ./cmd/rightsizing-replay < cases.ndjson > comparisons.ndjson
```

The default requires two effective days. Re-run with `-min-days 1` or `-min-days 3` for a sensitivity comparison. An input or output error exits nonzero. Discard partial output after an error.

## Prepare cases

Each input record describes one decision at a fixed point in time:

| Field | Meaning |
| --- | --- |
| `at` | Evaluation time of the captured scan in RFC 3339. |
| `workload` | Identifier including the cluster, namespace, kind and workload, sufficient to distinguish the cases being compared. |
| `replicas` | Replica count used by the scan to calculate the request change. |
| `row` | The original finalized CPU or memory row from that scan, including demand, current and recommended quantities, safety decision and confidence. Keep Radar's native units (CPU cores or memory bytes). |
| `evidence` | Optional collection record for this workload, container and resource. An omitted record produces an unavailable-evidence result. |

To inspect a saved Radar fleet scan before collecting additional evidence, convert its rows with `jq` (replace `example` with the source cluster):

```sh
jq -c --arg cluster example '
  .scannedAt as $at | .workloads[] as $w | $w.rows[] |
  {at: $at, workload: ([$cluster, $w.namespace, $w.kind, $w.name] | join("/")),
   replicas: $w.replicas, row: .}
' scan.json | go run ./cmd/rightsizing-replay
```

This exposes the captured decision and quantities. Qualifying a candidate action requires additional source evidence. Construct the case during the join on cluster, namespace, workload identity, container, resource and scan instant. The evaluator trusts this association in the supplied record.

Collect evidence at the same evaluation time, scope and workload revision as the row. Set `evidence.configSince` to the verified start of the active configuration. `directSource` attests to original sample timestamps. `representative` attests to the verified replica population and ownership. For a revision younger than the seven-day target window, set `targetCurrentConfigVerified` only after verifying the captured demand statistic uses measurements attributable to that configuration. If the store or inventory cannot prove a condition, omit its attestation or put the capture failure in `issue`.

For each eligible five-minute interval, identify the running replicas, verify their workload owner and configuration, and obtain actual CPU or memory sample times for all of them **inside that same UTC interval**. Skip an interval if any replica's latest measurement falls in an earlier one. In `sourceTimes`, supply the oldest of those replicas' latest original timestamps inside the interval. Count an interval only once. A range-query evaluation time, a carried-forward sample, and a workload-wide maximum by themselves do not establish representative source coverage. Keep the underlying query and inventory records with the replay for inspection. Historical inventory superseded by a rollout cannot be inferred from the current pod list.

For a Prometheus deployment, an instant query such as `container_memory_working_set_bytes{namespace="example",container="app"}[7d]` at the scan time returns the original samples. The selector here illustrates one family; add the verified cluster selector and workload Pod identities. Use an instant range vector of `container_cpu_usage_seconds_total` for CPU and require enough observations on the same series for a rate. Join source Pod UID to historical `kube_pod_owner` and `kube_replicaset_owner`, and check desired replicas, generation, and per-container requests and limits in each slot against the captured inventory. Keep the raw data and omit slots that fail the join. A new Pod with a proven equivalent owner, template, resources and image may retain credit. A changed request, limit, image or materially different template needs its own history. If historical equivalence is uncertain, start at the latest verifiable configuration. The same evidence contract applies to other stores that expose original samples.

## Read the comparison

The candidate counts distinct five-minute slots over the last 168 complete UTC hours. Each hour earns up to one credit at ten slots. Add the seven credits for each UTC hour of day; the lowest of the 24 totals gives effective days. A recurring afternoon outage therefore prevents a full cut. The most recent valid source sample must be at most one hour old. A reduction also needs at least 75% of the last 288 complete five-minute slots. A fresh observation in the current partial interval counts toward last-seen freshness. Depth updates on UTC hour boundaries, so a newly eligible workload can wait up to another hour for its next step.

For an eligible reduction, take `effectiveDays / 7` of the gap between the current request and Radar's existing rounded demand target. Round the resulting request upward to one millicore or one MiB. Existing review reasons take precedence. A CPU quota with unknown throttling requires review. The minimum evidence depth and reduction ramp apply to cuts. A workload that changes configuration needs fresh evidence for any numeric action. Its original seven-day target is usable only when the collector can attribute the measurements that produced it to that configuration. Additions and increases retain their captured target when fresh, attributable evidence is available.

The comparison includes the finalized `baselineRow` with its decision, observed usage, peak, limits and safety diagnostics, the collector’s `configSince` when available, and the candidate's `current`, shipped `baseline`, demand `target`, and proposed `next`. The baseline row keeps native units. The comparison quantities use mCPU or MiB as indicated by `candidate.unit`. When the candidate can size a cut, `direct` is the demand target and `flatHalf` is the larger of that target and half the current request rounded upward to the same unit. These alternatives hold eligibility fixed. Do not count either as observed savings.

`baselineVisibleSavings` counts only a shipped, actionable, material reduction. A review row can carry an estimate while contributing zero. Candidate `visibleSavings` is the positive replica-weighted change for a material reduction on this resource. A CPU cut must reach 50m, or a memory cut 64Mi, across replicas of this container in one workload. A 20m to 10m cut on one replica stays inspectable with status `immaterial`. With five replicas that same step yields 50m. Count reductions separately from increases and review decisions, and aggregate CPU and memory separately. Each output represents a resource row; one workload can have several rows, and a mixed-resource container can appear under increase or review in the live UI.

Compare future observations on held-out environments before choosing deployment thresholds. Request exceedance measures demand. It does not by itself measure latency, eviction, billing or behavior under a different HPA or placement.
