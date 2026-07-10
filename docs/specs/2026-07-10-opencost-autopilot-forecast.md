# OpenCost Autopilot Forecast Configuration

## Goal

Let operators exclude non-billable namespaces from Radar's OpenCost summary
and trend, and optionally stop node capacity pricing from replacing the
namespace allocation total.

Success means:

- configured namespaces are absent from summary and trend results
- excluded namespaces do not contribute to totals or the trend `other` series
- disabling the node-cost floor keeps the summary total equal to included
  namespace allocations
- OpenCost node details and raw Prometheus metrics remain unchanged
- existing installations preserve current behavior by default

The audience is operators running Radar against managed Kubernetes offerings
whose billing unit differs from node capacity, including GKE Autopilot.

## Scope And Interfaces

In scope:

- OpenCost summary and Prometheus trend computation
- Radar startup configuration and Helm chart arguments
- focused Go and chart rendering tests
- CLI and Helm documentation

Out of scope:

- cloud billing export ingestion
- currency conversion
- provider-specific namespace defaults
- Prometheus metric relabeling or deletion
- frontend layout changes

New CLI interfaces:

```text
--opencost-excluded-namespaces=<comma-separated namespace names>
--opencost-disable-node-cost-floor
```

New Helm values:

```yaml
opencost:
  excludedNamespaces: []
  disableNodeCostFloor: false
```

Both defaults preserve existing behavior. Namespace matching is exact after
trimming empty comma-separated values; Radar does not embed provider-specific
regular expressions or default exclusions.

## Data Flow And Behavior

```text
Helm values
  -> Radar CLI arguments
  -> immutable OpenCost handler configuration
  -> summary/trend compute options
  -> excluded namespace rows removed before totals and ranking
  -> optional node_total_hourly_cost floor bypass
  -> existing API response shapes
```

Exclusions apply to both REST-backed and Prometheus-backed summary computation.
They apply to Prometheus trend ranking and aggregation so excluded rows cannot
reappear under `other`. The workload endpoint remains queryable for an excluded
namespace, and the node endpoint remains unchanged.

When the node-cost floor is disabled, only the summary fallback that promotes
`node_total_hourly_cost` above namespace allocations is skipped. Node pricing
queries and node cost responses continue to work.

## Risks, Failure Modes, And Rollback

- A misspelled namespace remains included. The Helm render and live response
  verification must check exact configured names.
- Filtering after a Prometheus query could accidentally move excluded values
  into `other`. Focused trend tests must prove this cannot happen.
- A default change would alter every existing installation. Tests must prove
  unset values keep namespace totals and the node floor unchanged.
- Rollback is removal of the two Helm values or reversion to the previous Radar
  release; no metric or stored data migration is involved.

## Validation And Acceptance

Run:

```sh
go test ./opencost
make test-chart
go test ./...
go vet ./...
git diff --check
```

Render the chart with configured exclusions and verify the Deployment contains
the two expected arguments. Render defaults and verify neither argument is
present.

Acceptance cases:

- default summary still includes all namespaces and applies the node floor
- configured namespaces are absent from summary rows and total cost
- disabled node floor does not replace the included namespace sum
- excluded trend series are absent and do not contribute to `other`
- REST summary filtering matches Prometheus summary filtering
- node responses and raw metric collection are unaffected

## Rollout

Open a Draft PR against `skyhook-io/radar:main`, complete implementation and
validation, then mark it ready. GitOps consumption waits for the first official
Radar release containing the change. No private image release is used if the
upstream release is delayed.

## Task Breakdown

- [x] Commit this approved plan and open the Draft PR.
- [x] Add pure OpenCost exclusion and node-floor options with tests.
- [x] Wire CLI flags and immutable handler configuration.
- [x] Add Helm values, rendered arguments, and chart coverage.
- [x] Update operator documentation.
- [ ] Run focused and full validation.
- [ ] Push implementation and mark the upstream PR ready.
