import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { AlertBanner, EventsSection, ProblemAlerts, OperationalIssuesShownContext, RelatedResourcesSection, defaultConditionTone } from './drawer-components'
import type { Relationships } from '../../types'

const problems = [
  { color: 'red' as const, message: 'Application is Degraded' },
  { color: 'yellow' as const, message: 'Application is OutOfSync' },
]

describe('GKE node condition polarity', () => {
  it.each(['StoragePressureRootFileSystem', 'DPv2MigrationUnsupportedCNI', 'UnsupportedEBPFPrograms'])('%s is a problem only when True', type => {
    expect(defaultConditionTone({ type, status: 'False' })).toBe('ok')
    expect(defaultConditionTone({ type, status: 'True' })).toBe('fail')
    expect(defaultConditionTone({ type, status: 'Unknown' })).toBe('unknown')
  })
})

describe('ProblemAlerts', () => {
  it('renders every problem', () => {
    const html = renderToString(<ProblemAlerts problems={problems} />)
    expect(html).toContain('Application is Degraded')
    expect(html).toContain('Application is OutOfSync')
  })

  it('renders nothing when there are no problems', () => {
    expect(renderToString(<ProblemAlerts problems={[]} />)).toBe('')
  })

  // Regression guard: ProblemAlerts is used only by GitOps renderers, whose
  // problems the live-Issues pipeline does not comprehensively emit. It must NOT
  // suppress itself under the Operational-Issues context — doing so hid real
  // GitOps warnings (e.g. a manual Argo app's OutOfSync). Pod/Workload renderers
  // self-gate their own arrays; this component never should.
  it('still renders under OperationalIssuesShownContext (does not self-suppress)', () => {
    const html = renderToString(
      <OperationalIssuesShownContext.Provider value={true}>
        <ProblemAlerts problems={problems} />
      </OperationalIssuesShownContext.Provider>
    )
    expect(html).toContain('Application is Degraded')
    expect(html).toContain('Application is OutOfSync')
  })
})

function RelationshipsWithTruncatedChildren() {
  return <RelatedResourcesSection relationships={{ children: [{ kind: 'Pod', namespace: 'shop', name: 'web-0' }], ownership: { children: { complete: false, truncated: true } } }} />
}

describe('RelatedResourcesSection ownership', () => {
  const rs = { kind: 'ReplicaSet', group: 'apps', namespace: 'shop', name: 'web-7d9f8' }
  const cm = { kind: 'ConfigMap', namespace: 'shop', name: 'web-config' }
  const app = { kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'shop' }

  it('names further owners apart from the owner', () => {
    const html = renderToString(<RelatedResourcesSection relationships={{ owner: rs, owners: [rs, cm] }} onNavigate={() => {}} />)
    expect(html).toContain('Also Owned By')
    expect(html).toContain('web-config')
    expect(html.match(/web-7d9f8/g)).toHaveLength(1)
  })

  it('says how Managed By differs from Owner', () => {
    const html = renderToString(<RelatedResourcesSection relationships={{ managers: [app] }} onNavigate={() => {}} />)
    expect(html).toContain("Kubernetes&#x27; garbage collector follows owner references, not these links.")
  })

  it('puts a coverage hint on a button a keyboard can reach', () => {
    const html = renderToString(
      <RelatedResourcesSection relationships={{ children: [rs], ownership: { children: { complete: false, notChecked: [{ reason: 'read_error' }] } } }} />,
    )
    expect(html).toMatch(/<button type="button"[^>]*aria-label="Radar&#x27;s API discovery missed some API groups/)
  })

  it('shows management apart from ownership', () => {
    const html = renderToString(<RelatedResourcesSection relationships={{ managers: [app], manages: [rs] }} onNavigate={() => {}} />)
    expect(html).toContain('Managed By')
    expect(html).toContain('Manages')
    expect(html).not.toContain('Owner')
    expect(html).not.toContain('Children')
  })

  it('says which kinds a children list did not check', () => {
    const html = renderToString(
      <RelatedResourcesSection
        relationships={{
          children: [rs],
          ownership: { children: { complete: false, notChecked: [{ group: 'apps', kind: 'ControllerRevision', namespace: 'shop', reason: 'radar_kind_not_collected' }, { group: 'discovery.k8s.io', kind: 'EndpointSlice', namespace: 'shop', reason: 'radar_kind_not_collected' }] } },
        }}
      />,
    )
    expect(html).toContain('Radar doesn&#x27;t collect ControllerRevision or EndpointSlice objects, so none of those are listed.')
    expect(html).not.toContain('More children than shown')
  })

  it('says when a children list was cut short', () => {
    const html = renderToString(<RelationshipsWithTruncatedChildren />)
    expect(html).toContain('More children than shown.')
  })

  it('adds nothing to a complete children list, or one from an older Radar', () => {
    for (const relationships of [{ children: [rs], ownership: { children: { complete: true } } }, { children: [rs] }]) {
      const html = renderToString(<RelatedResourcesSection relationships={relationships} />)
      expect(html).toContain('Children')
      expect(html).not.toContain('Not checked')
    }
  })

  it('says why no owner is named', () => {
    const cases: Array<[Relationships['ownership'], string]> = [
      [{ primaryOwnerState: 'gone', ownerRef: rs, children: { complete: true } }, 'ReplicaSet web-7d9f8 no longer exists.'],
      [{ primaryOwnerState: 'replaced', ownerRef: rs, children: { complete: true } }, 'ReplicaSet web-7d9f8 was deleted and recreated; this object names the earlier one.'],
      [{ primaryOwnerState: 'unknown', ownerRef: rs, children: { complete: true } }, 'Couldn&#x27;t check ReplicaSet web-7d9f8.'],
      [
        { primaryOwnerState: 'unknown', ownerRef: { kind: 'Endpoints', namespace: 'shop', name: 'web' }, ownerReason: 'radar_kind_not_collected', children: { complete: true } },
        'Couldn&#x27;t check Endpoints web (Radar doesn&#x27;t collect it).',
      ],
      [{ primaryOwnerState: 'withheld', children: { complete: true } }, 'Not visible to you.'],
      [{ primaryOwnerState: 'unknown', children: { complete: true } }, 'Couldn&#x27;t check the owner it names.'],
      [{ primaryOwnerState: 'gone', ownerRef: { kind: '', namespace: '', name: '' }, children: { complete: true } }, 'The owner it names no longer exists.'],
      [{ primaryOwnerState: 'unresolvable', children: { complete: true } }, 'Its owner reference can&#x27;t be resolved in this cluster.'],
      [{ primaryOwnerState: 'excluded', children: { complete: true } }, 'Its owner reference names an Event'],
    ]
    for (const [ownership, text] of cases) {
      const html = renderToString(<RelatedResourcesSection relationships={{ ownership }} />)
      expect(html).toContain('Owner')
      expect(html).toContain(text)
    }
  })

  it('lists several owners without a controller as owners', () => {
    const html = renderToString(<RelatedResourcesSection relationships={{ owners: [rs, cm], ownership: { primaryOwnerState: 'several', children: { complete: true } } }} />)
    expect(html).toContain('<span>Owners<!-- --> (2)</span>')
    expect(html).toContain('None of them is its controller.')
    expect(html).not.toContain('<span>Owner</span>')
    expect(html).not.toContain('Also Owned By')
  })

  it('lists other owners beside a missing controller as also owned by', () => {
    const html = renderToString(<RelatedResourcesSection relationships={{ owners: [cm], ownership: { primaryOwnerState: 'gone', ownerRef: rs, children: { complete: true } } }} />)
    expect(html).toContain('<span>Owner</span>')
    expect(html).toContain('ReplicaSet web-7d9f8 no longer exists.')
    expect(html).toContain('Also Owned By')
  })

  it('says why owner and children could not be read at all', () => {
    expect(renderToString(<RelatedResourcesSection relationships={{ ownership: { unavailableReason: 'connection_changed', children: { complete: false } } }} />)).toContain(
      'Owner and children couldn&#x27;t be read: Radar&#x27;s connection changed meanwhile.',
    )
    expect(renderToString(<RelatedResourcesSection relationships={{ ownership: { unavailableReason: 'user_forbidden', children: { complete: false } } }} />)).toContain(
      'your access lets you open this object, but not list its kind here.',
    )
    for (const reason of ['object_not_found', 'read_error', 'cluster_disconnected']) {
      const html = renderToString(<RelatedResourcesSection relationships={{ ownership: { unavailableReason: reason, children: { complete: false } } }} />)
      expect(html).toContain('Owner and children couldn&#x27;t be read: ')
      expect(html).not.toContain('read: couldn')
    }
  })

  it('says outright when children could not be checked for an unusual reason, even with none found', () => {
    const loading = renderToString(
      <RelatedResourcesSection relationships={{ ownership: { children: { complete: false, notChecked: [{ kind: 'Pod', namespace: 'shop', reason: 'kind_sync_pending' }] } } }} />,
    )
    expect(loading).toContain('Children')
    expect(loading).toContain('Couldn&#x27;t check Pod (still loading).')
    const routine = renderToString(
      <RelatedResourcesSection relationships={{ ownership: { children: { complete: false, notChecked: [{ kind: 'Lease', namespace: 'shop', reason: 'radar_kind_not_collected' }] } } }} />,
    )
    expect(routine).toBe('')
    const leaf = renderToString(
      <RelatedResourcesSection kind="configmaps" relationships={{ ownership: { children: { complete: false, notChecked: [{ kind: 'Lease', namespace: 'shop', reason: 'radar_kind_not_collected' }] } } }} />,
    )
    expect(leaf).toBe('')
    const owner = renderToString(
      <RelatedResourcesSection kind="services" relationships={{ ownership: { children: { complete: false, notChecked: [{ kind: 'EndpointSlice', namespace: 'shop', reason: 'radar_kind_not_collected' }] } } }} />,
    )
    expect(owner).toContain('Children')
    expect(owner).toContain('No children among the kinds Radar checked.')
    const ownerComplete = renderToString(<RelatedResourcesSection kind="services" relationships={{ ownership: { children: { complete: true } } }} />)
    expect(ownerComplete).toBe('')
    const elsewhere = renderToString(
      <RelatedResourcesSection
        relationships={{ children: [rs], ownership: { children: { complete: false, notChecked: [{ kind: 'Pod', namespace: 'other', reason: 'radar_namespace_not_collected' }] } } }}
      />,
    )
    expect(elsewhere).toContain('Radar doesn&#x27;t collect every namespace, so children in the others aren&#x27;t listed.')
    expect(elsewhere).not.toContain('Couldn&#x27;t check')
  })

  it('says why a GitOps manager\'s member list could not be read', () => {
    const unread = renderToString(
      <RelatedResourcesSection relationships={{ ownership: { children: { complete: true }, manages: { membership: 'unreadable', unavailableReason: 'kind_sync_pending' } } }} />,
    )
    expect(unread).toContain('Manages')
    expect(unread).toContain('Couldn&#x27;t read the members its status records (still loading).')
    const noList = renderToString(<RelatedResourcesSection relationships={{ manages: [rs], ownership: { children: { complete: true }, manages: { membership: 'unreadable' } } }} />)
    expect(noList).not.toContain('Couldn&#x27;t read')
    expect(noList).toContain('Its status has no member list Radar can read now, so these are the resources the topology links to it.')
    const cut = renderToString(
      <RelatedResourcesSection relationships={{ manages: [rs], ownership: { children: { complete: true }, manages: { membership: 'unreadable', truncated: true } } }} />,
    )
    expect(cut).toContain('More members than shown.')
    const none = renderToString(<RelatedResourcesSection relationships={{ ownership: { children: { complete: true }, manages: { membership: 'unreadable' } } }} />)
    expect(none).toContain('Its status has no member list Radar can read now.')
    expect(none).not.toContain('these are the resources')
  })

  it('hints, without warning, at kinds API discovery missed', () => {
    const html = renderToString(
      <RelatedResourcesSection relationships={{ children: [rs], ownership: { children: { complete: false, notChecked: [{ reason: 'read_error' }] } } }} />,
    )
    expect(html).toContain('Radar&#x27;s API discovery missed some API groups, so children of their kinds aren&#x27;t listed.')
    expect(html).not.toContain('Couldn&#x27;t check')
  })

  it("says a remote manager's members aren't listed", () => {
    const html = renderToString(<RelatedResourcesSection relationships={{ ownership: { children: { complete: true }, manages: { membership: 'remote' } } }} />)
    expect(html).toContain('Manages')
    expect(html).toContain("Its destination isn&#x27;t this cluster as far as Radar can tell, so its members aren&#x27;t listed.")
  })

  it('says which direction Managed By comes from', () => {
    const html = renderToString(<RelatedResourcesSection relationships={{ managers: [app] }} onNavigate={() => {}} />)
    expect(html).toContain('Argo CD Applications or Flux objects whose status lists this resource, and other controllers that manage it without an owner reference.')
  })

  it("says outright what it couldn't check about this resource when it lists none", () => {
    const html = renderToString(
      <RelatedResourcesSection relationships={{ ownership: { children: { complete: true }, managers: { remote: 1, notChecked: [{ group: 'argoproj.io', kind: 'Application', reason: 'kind_sync_pending' }] } } }} />,
    )
    expect(html).toContain('Managed By')
    expect(html).toContain('1 manager lists it for a cluster Radar can&#x27;t confirm is this one; couldn&#x27;t check Application (still loading).')
  })

  it("keeps it to the hint beside a listed manager", () => {
    const html = renderToString(
      <RelatedResourcesSection relationships={{ managers: [app], ownership: { children: { complete: true }, managers: { remote: 1 } } }} onNavigate={() => {}} />,
    )
    expect(html).toMatch(/aria-label="[^"]*1 manager lists it for a cluster Radar can&#x27;t confirm is this one/)
    expect(html).not.toMatch(/>1 Manager lists it/)
  })

  it("keeps a manager kind discovery couldn't read to the hint, as it would show on every resource", () => {
    const gaps = [{ group: 'argoproj.io', kind: 'Application', reason: 'read_error' }, { group: 'kustomize.toolkit.fluxcd.io', kind: 'Kustomization', reason: 'read_error' }]
    const html = renderToString(<RelatedResourcesSection relationships={{ children: [rs], ownership: { children: { complete: true }, managers: { notChecked: gaps } } }} onNavigate={() => {}} />)
    expect(html).not.toContain('Managed By')
    const listed = renderToString(<RelatedResourcesSection relationships={{ managers: [app], ownership: { children: { complete: true }, managers: { notChecked: gaps } } }} onNavigate={() => {}} />)
    expect(listed).toContain('Radar couldn&#x27;t search Application objects, so those managers aren&#x27;t checked.')
    expect(listed).toContain('Radar couldn&#x27;t search Kustomization objects, so those managers aren&#x27;t checked.')
    expect(listed).not.toContain('Radar couldn&#x27;t search Application objects, so those managers aren&#x27;t checked.</div><div>Radar couldn&#x27;t search Application')
  })

  it('keeps an unreadable list to the hint, since it may name any resource', () => {
    const none = renderToString(<RelatedResourcesSection relationships={{ children: [rs], ownership: { children: { complete: true }, managers: { unreadable: 1 } } }} onNavigate={() => {}} />)
    expect(none).not.toContain('Managed By')
    const listed = renderToString(<RelatedResourcesSection relationships={{ managers: [app], ownership: { children: { complete: true }, managers: { unreadable: 2 } } }} onNavigate={() => {}} />)
    expect(listed).toMatch(/aria-label="[^"]*2 managers&#x27; member lists can&#x27;t be read/)
  })

  it('says when more than one manager lists a resource', () => {
    const other = { ...app, name: 'other' }
    const html = renderToString(<RelatedResourcesSection relationships={{ managers: [app, other] }} onNavigate={() => {}} />)
    expect(html).toContain('More than one manager lists or links to it.')
  })

  it('shows no Managed By when nothing lists the resource and the search was settled', () => {
    const html = renderToString(<RelatedResourcesSection relationships={{ children: [rs], ownership: { children: { complete: true } } }} onNavigate={() => {}} />)
    expect(html).not.toContain('Managed By')
  })

  it('says a recorded member list with nothing to show records none', () => {
    const html = renderToString(<RelatedResourcesSection relationships={{ ownership: { children: { complete: true }, manages: { membership: 'recorded' } } }} />)
    expect(html).toContain('Manages')
    expect(html).toContain('Its status records no members to show.')
  })

  it('counts recorded members it does not list', () => {
    const html = renderToString(
      <RelatedResourcesSection relationships={{ manages: [rs], ownership: { children: { complete: true }, manages: { membership: 'recorded', missing: 1, withheld: 2 } } }} />,
    )
    expect(html).toContain('Its status also lists 1 not in the cluster, 2 you can&#x27;t view.')
  })
})

describe('RelatedResourcesSection', () => {
  it('renders an associated Node', () => {
    const html = renderToString(
      <RelatedResourcesSection
        relationships={{ node: { kind: 'Node', namespace: '', name: 'worker-1' } }}
        onNavigate={() => {}}
      />,
    )

    expect(html).toContain('Related Resources')
    expect(html).toContain('Node')
    expect(html).toContain('worker-1')
    expect(html).toContain('<button')
  })
})

describe('Recent Events layout', () => {
  const events = Array.from({ length: 24 }, (_, i) => ({ id: String(i), source: 'k8s_event', eventType: 'Normal', reason: `Reason${i}`, timestamp: '2026-09-27T00:00:00Z' })) as any
  it('keeps drawer scrolling but lets fullscreen events expand in page flow', () => {
    const drawer = renderToString(<EventsSection events={events} />)
    const fullscreen = renderToString(<EventsSection events={events} fullscreen />)
    expect(drawer).toContain('max-h-64')
    expect(drawer).not.toContain('Show 16 more events')
    expect(fullscreen).not.toContain('max-h-64')
    expect(fullscreen).toContain('Show 16 more events')
    expect(fullscreen).toContain('Reason23')
  })
})

describe('dependency relationships', () => {
  it('renders a dependency-only response with the existing navigation control', () => {
    const html = renderToString(<RelatedResourcesSection relationships={{ dependencies: [{ kind: 'Issuer', group: 'cert-manager.io', namespace: 'team', name: 'ca' }] }} onNavigate={() => {}} />)
    expect(html).toContain('Depends On')
    expect(html).toContain('ca')
    expect(html).toContain('<button')
    expect(html).not.toContain('Scale Target')
  })

  it('renders the reverse dependency without calling it an autoscaler', () => {
    const html = renderToString(<RelatedResourcesSection relationships={{ dependents: [{ kind: 'Certificate', group: 'cert-manager.io', namespace: 'team', name: 'tls' }] }} />)
    expect(html).toContain('Required By')
    expect(html).toContain('tls')
    expect(html).not.toContain('Autoscaler')
  })
})

describe('versioned dependency projections', () => {
  const issuer = { kind: 'Issuer', group: 'cert-manager.io', namespace: 'team', name: 'ca' }
  const certificate = { kind: 'Certificate', group: 'cert-manager.io', namespace: 'team', name: 'tls' }

  it('presents mirrored configuration dependencies once under the specific labels', () => {
    const html = renderToString(<RelatedResourcesSection relationships={{ dependencies: [issuer], configRefs: [issuer], dependents: [certificate], consumers: [certificate] }} onNavigate={() => {}} />)
    expect(html).toContain('Depends On')
    expect(html).toContain('Required By')
    expect(html).not.toContain('Configuration')
    expect(html).not.toContain('Used By')
    expect(html.match(/<button/g)).toHaveLength(3)
  })

  it('preserves configuration and consumer groups when dependency fields are absent', () => {
    const html = renderToString(<RelatedResourcesSection relationships={{ configRefs: [issuer], consumers: [certificate] }} />)
    expect(html).toContain('Configuration')
    expect(html).toContain('Used By')
    expect(html).not.toContain('Depends On')
  })

  it('retains navigation controls for identically named resources from distinct API groups', () => {
    const html = renderToString(<RelatedResourcesSection relationships={{ dependencies: [issuer, { ...issuer, group: 'other.example.com' }, issuer] }} onNavigate={() => {}} />)
    expect(html.match(/<button/g)).toHaveLength(3)
  })
})

describe('namespace deletion condition polarity', () => {
  it.each([
    'NamespaceDeletionDiscoveryFailure',
    'NamespaceDeletionGroupVersionParsingFailure',
    'NamespaceDeletionContentFailure',
    'NamespaceContentRemaining',
    'NamespaceFinalizersRemaining',
  ])('%s treats True as a blocker and False as cleared', (type) => {
    expect(defaultConditionTone({ type, status: 'True' })).toBe('fail')
    expect(defaultConditionTone({ type, status: 'False' })).toBe('ok')
    expect(defaultConditionTone({ type, status: 'Unknown' })).toBe('unknown')
  })
})

describe('monitors projection', () => {
  it('renders Monitored By alone and deduplicates by API group', () => {
    const ref = { kind: 'Widget', group: 'one.example', namespace: 'demo', name: 'shared' }
    const html = renderToString(<RelatedResourcesSection relationships={{ monitors: [ref, ref, { ...ref, group: 'two.example' }] }} onNavigate={() => {}} />)
    expect(html).toContain('Related Resources')
    expect(html).toContain('Monitored By')
    expect(html.match(/<button/g)).toHaveLength(3)
  })
})


describe('monitorTargets projection', () => {
  it('renders Monitors alone and deduplicates by API group', () => {
    const ref = { kind: 'Widget', group: 'one.example', namespace: 'demo', name: 'shared' }
    const html = renderToString(<RelatedResourcesSection relationships={{ monitorTargets: [ref, ref, { ...ref, group: 'two.example' }] }} onNavigate={() => {}} />)
    expect(html).toContain('Related Resources')
    expect(html).toContain('Monitors')
    expect(html.match(/<button/g)).toHaveLength(3)
  })
})


describe('backends projection', () => {
  it('renders Routes To alone and deduplicates by API group', () => {
    const ref = { kind: 'Widget', group: 'one.example', namespace: 'demo', name: 'shared' }
    const html = renderToString(<RelatedResourcesSection relationships={{ backends: [ref, ref, { ...ref, group: 'two.example' }] }} onNavigate={() => {}} />)
    expect(html).toContain('Related Resources')
    expect(html).toContain('Routes To')
    expect(html.match(/<button/g)).toHaveLength(3)
  })
})


describe('routedFrom projection', () => {
  it('renders Routed From alone and deduplicates by API group', () => {
    const ref = { kind: 'Widget', group: 'one.example', namespace: 'demo', name: 'shared' }
    const html = renderToString(<RelatedResourcesSection relationships={{ routedFrom: [ref, ref, { ...ref, group: 'two.example' }] }} onNavigate={() => {}} />)
    expect(html).toContain('Related Resources')
    expect(html).toContain('Routed From')
    expect(html.match(/<button/g)).toHaveLength(3)
  })
})


describe('protects projection', () => {
  it('renders Selects alone and deduplicates by API group', () => {
    const ref = { kind: 'Widget', group: 'one.example', namespace: 'demo', name: 'shared' }
    const html = renderToString(<RelatedResourcesSection relationships={{ protects: [ref, ref, { ...ref, group: 'two.example' }] }} onNavigate={() => {}} />)
    expect(html).toContain('Related Resources')
    expect(html).toContain('Selects')
    expect(html.match(/<button/g)).toHaveLength(3)
  })
})


describe('healthChecks projection', () => {
  it('renders Health Checks alone and deduplicates by API group', () => {
    const ref = { kind: 'Widget', group: 'one.example', namespace: 'demo', name: 'shared' }
    const html = renderToString(<RelatedResourcesSection relationships={{ healthChecks: [ref, ref, { ...ref, group: 'two.example' }] }} onNavigate={() => {}} />)
    expect(html).toContain('Related Resources')
    expect(html).toContain('Health Checks')
    expect(html.match(/<button/g)).toHaveLength(3)
  })
})


describe('stagedPolicies projection', () => {
  it('renders Staged Policies alone and deduplicates by API group', () => {
    const ref = { kind: 'Widget', group: 'one.example', namespace: 'demo', name: 'shared' }
    const html = renderToString(<RelatedResourcesSection relationships={{ stagedPolicies: [ref, ref, { ...ref, group: 'two.example' }] }} onNavigate={() => {}} />)
    expect(html).toContain('Related Resources')
    expect(html).toContain('Staged Policies')
    expect(html.match(/<button/g)).toHaveLength(3)
  })
})


describe('routing version skew', () => {
  it('keeps older Services, Gateways, Routes and Pods fields visible', () => {
    const html = renderToString(<RelatedResourcesSection relationships={{ services: [{kind: 'Service', namespace: 'demo', name: 'web'}], gateways: [{kind: 'HTTPRoute', group: 'gateway.networking.k8s.io', namespace: 'demo', name: 'old-route'}], routes: [{kind: 'IngressRoute', group: 'traefik.io', namespace: 'demo', name: 'route'}], pods: [{kind: 'Deployment', group: 'apps', namespace: 'demo', name: 'old-target'}] }} />)
    for (const label of ['Services', 'Gateways', 'Routes', 'Pods', 'old-route', 'old-target']) expect(html).toContain(label)
  })
})

describe('related resource chips', () => {
  it('tells a Knative Service apart from a core Service', () => {
    const html = renderToString(<RelatedResourcesSection relationships={{
      services: [{ kind: 'Service', namespace: 'team', name: 'web-private' }],
      routedFrom: [{ kind: 'Service', group: 'serving.knative.dev', namespace: 'team', name: 'web' }],
    }} onNavigate={() => {}} />)
    expect(html).toContain('>svc<!-- -->/</span>web-private<')
    expect(html).toContain('>ksvc<!-- -->/</span>web<')
  })
})


describe('AlertBanner optional actions', () => {
  it('retains the title-only default without introducing a layout wrapper', () => {
    const html = renderToString(<AlertBanner variant="info" title="Paused" />)
    expect(html).toMatch(/<div class="text-sm font-medium [^"]*">Paused<\/div>/)
    expect(html).not.toContain('justify-between'); expect(html).not.toContain('flex-1')
  })
  it.each([undefined, 'Check connection'])('places the optional action next to the title, retaining the body (%s)', message => {
    const html = renderToString(<AlertBanner variant="warning" title="Metrics unavailable" message={message} action={<button>Open settings</button>} />)
    expect(html).toContain('Metrics unavailable'); expect(html).toContain('<button>Open settings</button>')
    expect(html).toContain('flex-wrap'); if (message) expect(html).toContain(message)
  })
})


describe('RelatedResourcesSection capped lists', () => {
  it('marks a list the server cut short as a lower bound', () => {
    const pods = Array.from({ length: 12 }, (_, i) => ({ kind: 'Pod', namespace: 'shop', name: `web-${i}` }))
    const text = (relationships: Relationships) => renderToString(<RelatedResourcesSection relationships={relationships} onNavigate={() => {}} />).replace(/<!-- -->/g, '')
    const capped = text({ children: pods, ownership: { children: { complete: false, truncated: true } } })
    expect(capped).toContain('Children (12+)')
    expect(capped).toContain('Show the 12 listed')
    const whole = text({ children: pods, ownership: { children: { complete: true } } })
    expect(whole).toContain('Children (12)')
    expect(whole).toContain('Show all 12')
  })
})
