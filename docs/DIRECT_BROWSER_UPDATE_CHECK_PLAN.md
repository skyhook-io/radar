# Direct browser update-check plan

Status: proposal only. The current implementation relays browser-attributed
checks through the Radar backend. This document describes how a future switch
to direct browser checks should work if its benefits justify the added trust
and compatibility costs.

## Premise

Radar measures two different approximations:

1. Active installations come from the ordinary Radar backend update check.
2. Active browser profiles on a standalone in-cluster installation come from
   a separate browser-attributed update check.

A direct browser design must not replace, suppress, delay, or share cache state
with the backend check. The backend check remains the source for active
installations in local, Desktop, and standalone in-cluster modes.

The browser metric estimates browser profiles, not people. Shared profiles
collapse, multiple profiles overcount, and always-open tabs can be missed. The
metric does not need cross-install deduplication or cross-day retention.

## When to reconsider the relay

Switch only if evidence shows that one of these benefits matters:

- pod egress restrictions materially suppress browser-profile measurements;
- country-level viewer geography is valuable enough to justify direct traffic;
- removing the backend relay materially improves operational reliability.

Do not switch merely to simplify receiver plumbing. Direct traffic is more
visible to users, privacy tools, browser policies, and network administrators.

## Required journeys

### Local CLI and Desktop

- Continue calling the backend `/version-check` endpoint.
- Do not make a browser-attributed request.

### Standalone in-cluster

- Always call backend `/version-check` for release information and the active
  installation signal.
- Independently obtain a small same-origin check context from Radar.
- Make at most one direct browser attempt per API base, browser profile, and UTC
  day.

### Radar Cloud

- Do not emit the OSS browser event.
- Keep Cloud out of the OSS active-installation series.

### Build channels

- Stable, prerelease, and custom builds remain measurable and carry an explicit
  `build_channel`.
- Development builds (`dev`, `dev-*`, dirty builds, raw commit SHAs, and
  git-describe builds) do not emit either usage event.

## Request flow

1. The frontend starts the ordinary `GET /api/version-check`.
2. In standalone in-cluster mode only, it requests
   `GET /api/version-check/context`.
3. The context response contains only the fields already used by update checks:
   Radar version, OS/architecture, install method, deployment mode, Deployment
   creation timestamp when available, and build channel. It does not contain
   authentication state, username, cluster identity, or Kubernetes data.
4. If the build is eligible and the browser has not attempted that day, the
   frontend records the UTC day in localStorage before sending.
5. The browser sends a credential-free request directly to a report-only
   releases endpoint. The ordinary backend check proceeds regardless of any
   context or direct-request failure.

The direct endpoint returns `204 No Content`. It does not proxy GitHub release
data; version rendering continues to use the backend response and fallback.

## Browser state, identity, and frequency

- Store only the last attempted UTC day, scoped by API base and installation
  timestamp when available.
- Do not create or transmit a browser ID, cookie, fingerprint, UUID, network
  hash, or cross-day key.
- Mark the day before sending. Do not retry and do not require an
  acknowledgment.
- Trigger on initial app load. Do not add hourly polling. A continuously open
  tab crossing midnight is an accepted undercount.
- Storage failure suppresses the browser event without affecting Radar.

This deliberately chooses best-effort delivery. Transient failures undercount,
and rare multi-tab races may overcount.

## Direct endpoint contract

Use a dedicated report-only endpoint, for example:

```
GET https://releases.skyhook.io/radar/browser-check
  ?v=1.12.3
  &os=linux
  &arch=amd64
  &method=direct
  &mode=in-cluster
  &t=1700000000
  &day=2026-08-30
  &build_channel=stable
```

Requirements:

- `Access-Control-Allow-Origin: *`;
- no credentials, cookies, authorization headers, or preflight-only headers;
- strict length and value validation;
- development builds ignored;
- asynchronous, bounded event capture followed by `204`;
- no GitHub lookup and no release payload;
- no browser-generated identifier or retry protocol.

The endpoint is public and unauthenticated, so the metric remains approximate
and forgeable. Monitor volume and obvious anomalies; do not present it as an
auditable count.

## Viewer geography

Direct requests make coarse viewer geography possible, but it is optional.
If enabled:

- retain country only;
- derive it at the edge from the connection;
- do not store or forward the raw IP;
- disable PostHog GeoIP enrichment for the captured event;
- do not retain region, city, ASN, or a network-derived identifier.

Country collection requires a separate explicit product/privacy sign-off. The
initial direct implementation should otherwise omit geography.

## Event and query semantics

`radar_update_check` remains the active-installation event. Its headline series
is daily unique receiver distinct IDs for intended OSS modes, using
`installation_id` when the Deployment timestamp is available and the existing
coarser fallback otherwise.

`radar_browser_update_check` is counted as raw accepted events per UTC day,
optionally segmented by `installation_id`. Local browser gating makes one
event approximate one active browser profile for that installation/day. Do not
interpret PostHog persons or `distinct_id` as unique viewers.

Historical charts must annotate the rollout because development exclusion,
Cloud exclusion, and browser-direct delivery can all change the level without
organic usage changing.

## Privacy and copy review

Keep “no cluster telemetry” claims: the request contains no resources,
manifests, logs, events, metrics, cluster name, or user identity.

Before implementation, review the exact configuration-documentation and
marketing copy. Describe the request as an additional direct update check, not
as a separate telemetry subsystem. State that it has no stable browser
identifier and is attempted at most daily. Do not add broader README or
security-page language unless an existing claim becomes false.

## Rollout sequence

1. Implement and deploy the direct receiver endpoint with capture disabled.
2. Verify CORS from HTTP and HTTPS test origins, including private-network and
   ad-blocked failure cases.
3. Enable receiver capture and validate channel classification and field
   minimization.
4. Ship the Radar context endpoint and browser request while preserving the
   backend check.
5. Annotate the measurement rollout and compare relay/direct delivery before
   removing the relay endpoint.

## Required tests

- Backend `/version-check` is called in standalone in-cluster mode regardless
  of direct-request outcome.
- Local, Desktop, and Cloud make no direct browser request.
- One attempt per API base/profile/UTC day; next day attempts again.
- No timer, retry, acknowledgment, UUID, fingerprint, auth state, or network
  identifier.
- Development builds are excluded; stable, prerelease, and custom channels
  match the backend classifier.
- Context and receiver reject malformed or oversized input.
- Receiver returns CORS-enabled `204` without calling GitHub.
- Direct failure never changes cached or rendered update information.
- Geography is absent unless separately approved; if approved, only country is
  retained and raw IP is not captured.
