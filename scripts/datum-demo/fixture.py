#!/usr/bin/env python3
"""Disposable Milo fixture. Status patches are explicitly synthetic."""
import argparse
import json
import os
import pathlib
import secrets
import subprocess
import urllib.request

DNS_PIN = '32aaaef43c996eb4740131194a213dc772d0f856'
NET_PIN = '346077b6c854e196e15b782c175d1bb455005d96'
COMPUTE_PIN = '7ce96d27d7e88b25707cba6fc0201f2e2e0892af'
DNS = 'dns.networking.miloapis.com'
NET = 'networking.datumapis.com'
RM = 'resourcemanager.miloapis.com'
CRDS = [
    ('datum-cloud/dns-operator', DNS_PIN, 'config/crd/bases', DNS, plural)
    for plural in ['dnszones', 'dnsrecordsets', 'dnszoneclasses']
] + [
    ('datum-cloud/network-services-operator', NET_PIN, 'config/crd/bases', NET, plural)
    for plural in ['domains', 'httpproxies', 'connectors', 'connectorclasses', 'connectoradvertisements',
                   'networks', 'networkcontexts', 'networkbindings', 'subnets', 'subnetclaims', 'networkservices']
] + [
    ('datum-cloud/compute', COMPUTE_PIN, 'config/base/crd/bases', 'compute.datumapis.com', plural)
    for plural in ['instances', 'workloads']
]

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('command', choices=['up', 'install', 'seed', 'all'])
parser.add_argument('--context', default='radar-test-nonprod')
parser.add_argument('--milo-kubeconfig', type=pathlib.Path)
parser.add_argument('--state-dir', type=pathlib.Path, default=pathlib.Path('/private/tmp/radar-datum-demo'))
parser.add_argument('--project', default='radar-datum-demo')
parser.add_argument('--project-plane', action='store_true', help='Seed a project-scoped Milo kubeconfig; omit root Organization/Project objects')
args = parser.parse_args()
args.state_dir.mkdir(parents=True, exist_ok=True)
os.chmod(args.state_dir, 0o700)


def run(command, data=None, capture=False):
    result = subprocess.run(command, input=data, text=True, capture_output=capture, check=True)
    return result.stdout if capture else None


def milo(*command, data=None, capture=False):
    if not args.milo_kubeconfig:
        parser.error('--milo-kubeconfig is required for install/seed/all')
    return run(['kubectl', '--kubeconfig', str(args.milo_kubeconfig), *command], data, capture)


def ensure_milo():
    version = json.loads(milo('get', '--raw', '/version', capture=True))
    if 'milo' not in version['gitVersion']:
        raise SystemExit('Refusing to install fixtures into an ordinary Kubernetes cluster')


def up():
    if args.context != 'radar-test-nonprod':
        raise SystemExit('This fixture deploys only to context radar-test-nonprod, namespace milo-system')
    namespace = {'apiVersion': 'v1', 'kind': 'Namespace', 'metadata': {'name': 'milo-system'}}
    eks = ['kubectl', '--context', args.context, '-n', 'milo-system']
    run([*eks, 'apply', '-f', '-'], json.dumps(namespace))
    cert = args.state_dir / 'tls.crt'
    key = args.state_dir / 'tls.key'
    if not cert.exists():
        run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '7',
             '-keyout', str(key), '-out', str(cert), '-subj', '/CN=radar-datum-demo',
             '-addext', 'subjectAltName=IP:127.0.0.1,DNS:milo-apiserver.milo-system.svc.cluster.local'])
        os.chmod(key, 0o600)
    tokens = args.state_dir / 'tokens.csv'
    if not tokens.exists():
        tokens.write_text(f'{secrets.token_urlsafe(32)},radar-fixture,radar-fixture,"system:masters"\n')
        os.chmod(tokens, 0o600)
    for name, files in [('milo-apiserver-tls', {'tls.crt': cert, 'tls.key': key, 'ca.crt': cert}),
                        ('milo-apiserver-auth-tokens', {'tokens.csv': tokens})]:
        manifest = run([*eks, 'create', 'secret', 'generic', name,
                        *[f'--from-file={k}={v}' for k, v in files.items()], '--dry-run=client', '-o', 'json'], capture=True)
        run([*eks, 'apply', '--server-side', '-f', '-'], manifest)
    run([*eks, 'apply', '-f', str(pathlib.Path(__file__).with_name('milo.yaml'))])
    run([*eks, 'rollout', 'status', 'deployment/milo-apiserver', '--timeout=120s'])
    kubeconfig = args.state_dir / 'milo.kubeconfig'
    import base64
    kubeconfig.write_text(json.dumps({'apiVersion': 'v1', 'kind': 'Config',
        'clusters': [{'name': 'milo-fixture', 'cluster': {'server': 'https://127.0.0.1:16445', 'certificate-authority-data': base64.b64encode(cert.read_bytes()).decode()}}],
        'users': [{'name': 'fixture-admin', 'user': {'token': tokens.read_text().split(',')[0]}}],
        'contexts': [{'name': 'milo-fixture', 'context': {'cluster': 'milo-fixture', 'user': 'fixture-admin'}}], 'current-context': 'milo-fixture'}))
    os.chmod(kubeconfig, 0o600)
    print(f'Private kubeconfig: {kubeconfig}. Start your own port-forward on 16445+, then install and seed.')


def install():
    ensure_milo()
    target = args.state_dir / 'crds'
    target.mkdir(exist_ok=True)
    for repo, pin, prefix, group, plural in CRDS:
        path = target / f'{group}_{plural}.yaml'
        url = f'https://raw.githubusercontent.com/{repo}/{pin}/{prefix}/{path.name}'
        with urllib.request.urlopen(url, timeout=30) as response:
            path.write_bytes(response.read())
        milo('apply', '--server-side', '-f', str(path))
    milo('wait', '--for=condition=Established', 'crd', *[f'{p}.{g}' for _, _, _, g, p in CRDS], '--timeout=60s')


def qualified(obj):
    group = obj['apiVersion'].split('/')[0] if '/' in obj['apiVersion'] else ''
    return obj['kind'] + ('.' + group if group else '')


def apply(obj):
    obj['metadata'].setdefault('annotations', {})['radar.skyhook.io/fixture'] = 'datum-demo'
    milo('apply', '--server-side', '-f', '-', data=json.dumps(obj))
    return json.loads(milo('get', qualified(obj), obj['metadata']['name'],
        *(['-n', obj['metadata']['namespace']] if obj['metadata'].get('namespace') else []), '-o', 'json', capture=True))


def condition(typ, value='True', reason='Reconciled', message=None, generation=1):
    if message is None:
        message = 'Synthetic fixture: controller reconciliation succeeded' if value == 'True' else f'Synthetic fixture: {typ} {reason}'
    return {'type': typ, 'status': value, 'reason': reason, 'message': message,
            'observedGeneration': generation, 'lastTransitionTime': '2026-10-04T12:00:00Z'}


def object_(kind, name, spec, group=NET, version='v1alpha', ns='datum-demo'):
    return {'apiVersion': f'{group}/{version}', 'kind': kind, 'metadata': {'name': name, **({'namespace': ns} if ns else {})}, 'spec': spec}


def seeded(obj, status=None):
    obj['metadata'].setdefault('annotations', {})['radar.skyhook.io/synthetic-status'] = 'true'
    live = apply(obj)
    if status is not None:
        for c in status.get('conditions', []):
            c['observedGeneration'] = live['metadata']['generation']
        milo('patch', qualified(obj), obj['metadata']['name'],
            *(['-n', obj['metadata']['namespace']] if obj['metadata'].get('namespace') else []),
            '--subresource=status', '--type=merge', '-p', json.dumps({'status': status}))


def seed():
    ensure_milo()
    apply({'apiVersion': 'v1', 'kind': 'Namespace', 'metadata': {'name': 'datum-demo'}})
    if not args.project_plane:
        apply(object_('Organization', 'radar-datum-fixture', {}, RM, 'v1alpha1', None))
        apply(object_('Project', args.project, {'ownerRef': {'kind': 'Organization', 'name': 'radar-datum-fixture'}}, RM, 'v1alpha1', None))
    seeded(object_('DNSZoneClass', 'fixture-static', {'controllerName': 'fixture-powerdns', 'nameServerPolicy': {'mode': 'Static', 'static': {'servers': ['ns1.example.test', 'ns2.example.test']}}}, DNS, 'v1alpha1', None))
    seeded(object_('DNSZone', 'example-test', {'domainName': 'example.test', 'dnsZoneClassName': 'fixture-static'}, DNS, 'v1alpha1'),
           {'conditions': [condition('Accepted'), condition('Programmed')], 'nameservers': ['ns1.example.test', 'ns2.example.test'], 'recordCount': 3, 'domainRef': {'name': 'example-test'}})
    seeded(object_('Domain', 'example-test', {'domainName': 'example.test'}),
           {'conditions': [condition('ValidDomain'), condition('Verified'), condition('VerifiedHTTP', 'False', 'RecordNotFound', 'Synthetic fixture: HTTP verification was not needed')], 'nameservers': [{'hostname': 'ns1.example.test'}], 'verification': {'dnsRecord': {'name': '_datum.example.test', 'type': 'TXT', 'content': 'fixture-verification-secret'}, 'httpToken': {'url': 'https://example.test/.well-known/datum', 'body': 'fixture-http-secret'}}})
    for name, address, value, reason in [('www', '192.0.2.10', 'True', 'Reconciled'), ('broken', '192.0.2.11', 'False', 'ProviderRejected')]:
        seeded(object_('DNSRecordSet', name, {'dnsZoneRef': {'name': 'example-test'}, 'recordType': 'A', 'records': [{'name': f'{name}.example.test', 'ttl': 300, 'a': {'content': address}}]}, DNS, 'v1alpha1'),
               {'conditions': [condition('Accepted'), condition('Programmed', value, reason)], 'recordSets': [{'name': f'{name}.example.test', 'conditions': [condition('RecordProgrammed', value, reason, 'Synthetic fixture: record programming failed' if value=='False' else 'Synthetic fixture: record programmed')]}]})
    seeded(object_('ConnectorClass', 'fixture-relay', {'controllerName': 'fixture-relay'}, NET, 'v1alpha1', None))
    for name, value in [('edge-healthy', 'True'), ('edge-offline', 'False')]:
        seeded(object_('Connector', name, {'connectorClassName': 'fixture-relay', 'capabilities': [{'type': 'ConnectTCP'}]}, NET, 'v1alpha1'),
               {'conditions': [condition('Accepted'), condition('Ready', value, 'Connected' if value=='True' else 'ConnectionLost', f'Synthetic fixture: connector {name} state')], 'leaseRef': {'name': name}, 'capabilities': [{'type': 'ConnectTCP', 'conditions': [condition('Accepted'), condition('Ready', value, 'Connected' if value=='True' else 'ConnectionLost')]}]})
    seeded(object_('ConnectorAdvertisement', 'origin-advertisement', {'connectorRef': {'name': 'edge-healthy'}, 'layer4': [{'name': 'origin', 'services': [{'address': 'origin.example.test', 'ports': [{'name': 'https', 'port': 443, 'protocol': 'TCP'}]}]}]}, NET, 'v1alpha1'),
           {'conditions': [condition('Accepted')]})
    for name, connector, value, reason in [('www','edge-healthy','True','Reconciled'), ('broken','edge-offline','False','BackendUnavailable'), ('pending','edge-healthy','Unknown','Pending')]:
        seeded(object_('HTTPProxy', name, {'hostnames': [f'{name}.example.test'], 'rules': [{'name': 'primary', 'backends': [{'connector': {'name': connector}, 'endpoint': 'https://origin.example.test'}]}]}),
               {'conditions': [condition('Accepted'), condition('Programmed', value, reason)], 'hostnameStatuses': [{'hostname': f'{name}.example.test', 'conditions': [condition('Verified'), condition('DNSRecordProgrammed', value, reason), condition('Available', value, reason), condition('CertificateReady', value, 'Pending' if value!='True' else 'CertificateIssued')]}], 'canonicalHostname': f'{name}.fixture.datum.net', 'addresses': [{'type': 'IPAddress', 'value': '192.0.2.20'}]})
    seeded(object_('HTTPProxy','unobserved',{'hostnames':['unknown.example.test'],'rules':[{'backends':[{'endpoint':'https://unknown-origin.example.test'}]}]}))
    seeded(object_('Network', 'edge-network', {'ipam': {'mode': 'Auto'}, 'ipFamilies': ['IPv6']}), {'conditions': [condition('Ready')], 'ipam': {'ipv6Prefix': '2001:db8::/48'}})
    location = {'name': 'fixture-location'}
    seeded(object_('NetworkContext', 'edge-context', {'network': {'name': 'edge-network'}, 'location': location, 'ipFamilies': ['IPv6']}), {'conditions': [condition('Ready')]})
    seeded(object_('NetworkBinding', 'edge-binding', {'network': {'name': 'edge-network'}, 'location': location, 'consumer': {'apiGroup': 'compute.datumapis.com', 'kind': 'Instance', 'name': 'origin'}}), {'networkContextRef': {'name': 'edge-context', 'namespace': 'datum-demo'}, 'conditions': [condition('Ready')]})
    subnet = {'subnetClass': 'private', 'networkContext': {'name': 'edge-context'}, 'location': location, 'ipFamily': 'IPv6', 'startAddress': '2001:db8::', 'prefixLength': 64}
    seeded(object_('Subnet', 'edge-subnet', subnet), {'startAddress': '2001:db8::', 'prefixLength': 64, 'conditions': [condition('Ready'), condition('Programmed')]})
    seeded(object_('SubnetClaim', 'edge-claim', subnet), {'subnetRef': {'name': 'edge-subnet'}, 'startAddress': '2001:db8::', 'prefixLength': 64, 'conditions': [condition('Ready')]})
    seeded(object_('NetworkService', 'origin-service', {'networkInterfaces': {'selector': {'matchLabels': {'app': 'fixture-origin'}}}, 'ports': [{'name': 'https', 'protocol': 'TCP', 'port': 443}]}), {'conditions': [condition('MembersResolved'), condition('Ready')], 'summary': {'locations': 1, 'members': 2, 'healthy': 1}, 'locations': [{'name': 'fixture-location', 'members': 2, 'healthy': 1, 'serving': True}]})
    apply({'apiVersion': 'discovery.k8s.io/v1', 'kind': 'EndpointSlice', 'metadata': {'name': 'origin-endpoints', 'namespace': 'datum-demo'}, 'addressType': 'IPv4', 'endpoints': [{'addresses': ['192.0.2.30'], 'conditions': {'ready': True}}, {'addresses': ['192.0.2.31'], 'conditions': {'ready': False}}], 'ports': [{'name': 'https', 'protocol': 'TCP', 'port': 443}]})
    seeded(object_('HTTPProxy', 'slice-backend', {'hostnames': ['slice.example.test'], 'rules': [{'backends': [{'instance': {'name': 'origin-endpoints', 'port': 443}}]}]}), {'conditions': [condition('Accepted'), condition('Programmed')], 'hostnameStatuses': [{'hostname': 'slice.example.test', 'conditions': [condition('Verified'), condition('Available'), condition('CertificateReady')]}]})
    seeded(object_('HTTPProxy', 'service-backend', {'hostnames': ['service.example.test'], 'rules': [{'backends': [{'networkService': {'name': 'origin-service', 'port': 'https'}}]}]}), {'conditions': [condition('Accepted'), condition('Programmed')], 'hostnameStatuses': [{'hostname': 'service.example.test', 'conditions': [condition('Verified'), condition('Available'), condition('CertificateReady')]}]})
    instance_spec = {'runtime': {'resources': {'instanceType': 'datumcloud-d1-standard-2'}, 'sandbox': {'containers': [{'name': 'origin', 'image': 'nginx:1.27.5'}]}}, 'networkInterfaces': [{'name': 'eth0', 'network': {'name': 'edge-network'}, 'ipFamilies': ['IPv6']}]}
    seeded(object_('Instance', 'origin', instance_spec, 'compute.datumapis.com'), {'conditions': [condition('Available'), condition('Progressing', 'False', 'Stable')]})
    seeded(object_('Workload', 'origin-workload', {'template': {'spec': instance_spec}, 'placements': [{'name': 'fixture', 'locations': [location], 'scaleSettings': {'minReplicas': 2, 'instanceManagementPolicy': 'OrderedReady'}}]}, 'compute.datumapis.com'), {'conditions': [condition('Available', 'False', 'InstancesProvisioning')], 'desiredReplicas': 2, 'replicas': 1, 'readyReplicas': 0, 'updatedReplicas': 1, 'currentReplicas': 0, 'deployments': 1})
    import datetime
    now = datetime.datetime.now(datetime.timezone.utc)
    for name, age in [('edge-healthy', 0), ('edge-offline', 7200)]:
        renew = (now - datetime.timedelta(seconds=age)).isoformat().replace('+00:00', 'Z')
        apply({'apiVersion': 'coordination.k8s.io/v1', 'kind': 'Lease', 'metadata': {'name': name, 'namespace': 'datum-demo', 'annotations': {'radar.skyhook.io/synthetic-status': 'true'}}, 'spec': {'holderIdentity': 'synthetic-fixture-agent', 'renewTime': renew, 'leaseDurationSeconds': 3600}})
    print('Fixture seeded. Every patched controller status is marked synthetic.')


if args.command == 'up': up()
elif args.command == 'install': install()
elif args.command == 'seed': seed()
else: install(); seed()
