import { describe, expect, it } from 'vitest'
import { isDiagnoseKind } from './WorkloadView'

/**
 * The rule that decides whether a resource gets a Reachability tab.
 *
 * `isDiagnoseKind` alone excluded every workload kind, so a Deployment had no
 * Reachability tab at all - the operator got a link to the Service and had to
 * navigate away and restart the investigation there. The tab now also opens for
 * a resource that is SERVED by an entry kind, which is what makes "reachability
 * for a Service or workload" true of workloads.
 */
const tabVisible = (kind: string, group: string | undefined, servingServices: unknown[]): boolean =>
  isDiagnoseKind(kind, group) || servingServices.length > 0

const svc = [{ kind: 'Service', namespace: 'prod', name: 'web' }]

describe('who gets a Reachability tab', () => {
  it('entry kinds trace themselves, with no Service needed', () => {
    expect(tabVisible('Service', '', [])).toBe(true)
    expect(tabVisible('Ingress', 'networking.k8s.io', [])).toBe(true)
    expect(tabVisible('HTTPRoute', 'gateway.networking.k8s.io', [])).toBe(true)
  })

  it('a workload behind a Service now gets one', () => {
    for (const kind of ['Deployment', 'StatefulSet', 'DaemonSet', 'Pod']) {
      expect(tabVisible(kind, 'apps', svc)).toBe(true)
    }
  })

  // Nothing selects it, so there is no path to trace and no honest tab to show.
  it('a workload nothing exposes does not', () => {
    expect(tabVisible('Deployment', 'apps', [])).toBe(false)
  })

  // A CRD sharing a core kind name must not enable a trace against the wrong
  // object - the group check that guards this is load-bearing, not incidental.
  it('a Knative Service is not a core Service', () => {
    expect(tabVisible('Service', 'serving.knative.dev', [])).toBe(false)
  })

  // ...but if something does select its pods, it still earns the tab through
  // the serving Service, exactly like any other workload.
  it('a same-named CRD still qualifies via its serving Service', () => {
    expect(tabVisible('Service', 'serving.knative.dev', svc)).toBe(true)
  })
})
