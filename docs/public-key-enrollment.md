# Enroll Radar with a customer-owned key pair

Public-key enrollment lets Radar prove it holds your private key without sending
that key to Hub. Register the public half in the intended Hub organization. Put
the private half in a Secret in each cluster where Radar runs.

This requires a Hub with public-key enrollment enabled and an agent/chart release
containing the signing code. The planned first agent version is **1.20.3**; it is
an unreleased release candidate until that code is published. Existing 1.20.2
images do not support these settings. Release preparation must update this floor
if the change ships under another version. Source builds can use the local chart
with a matching source-built image. This page does not imply a release exists.

## Generate and register the public key

Generate the pair locally, without contacting Kubernetes or Hub:

```sh
radar cloud enrollment-keygen --out radar-enrollment.pem
```

The command creates a PKCS#8 Ed25519 private file with mode `0600`, and a
SubjectPublicKeyInfo public file named `radar-enrollment.pem.pub`. It refuses to
overwrite either file and never prints private material. On Windows, also use a
directory whose ACL permits only the account that manages this key.

In Hub, open **Settings > Enrollment keys > Register public key**. Paste only
the `.pub` file and choose the enrollment policy. New public keys default to
waiting for an owner to approve new clusters. Use the organization ID from the
setup instructions. Radar signs that target ID so registering the same public
key in another organization cannot redirect your cluster.

For a system with OpenSSL 3, the equivalent generation commands are:

```sh
openssl genpkey -algorithm ED25519 -out radar-enrollment.pem
chmod 600 radar-enrollment.pem
openssl pkey -in radar-enrollment.pem -pubout -out radar-enrollment.pem.pub
```

The LibreSSL executable included with some macOS versions does not support that
generation command. Use Radar's command or OpenSSL 3. SSH private-key format,
encrypted PKCS#8 files, RSA and P-256 keys are not accepted by this version.

## Create the private-key Secret

Choose the intended cluster and a namespace dedicated to Radar. Create the
namespace if it does not exist, then create the Secret from the private file:

```sh
kubectl -n radar create secret generic radar-hub-enrollment \
  --from-file=private-key=radar-enrollment.pem \
  --dry-run=client -o yaml | kubectl apply -f -
```

Run these commands only against the cluster you intend to connect. The private
key stays in the Secret; it is not an argument value or a Helm value. Configure
Secret encryption at rest and restrict who can read that Secret. A shared private
key lets all of its holders enroll under its policy. Use separate keys when you
need to limit the effect of a cluster compromise.

Use these values with the signing-capable chart and image:

```yaml
cloud:
  enabled: true
  hubURL: https://hub.example.com
  enrollment:
    organization: org_from_hub_setup
    publicKey:
      existingSecret: radar-hub-enrollment
      existingSecretKey: private-key
```

Do not set `cloud.url`, `cloud.token`, `cloud.existingSecret`,
`cloud.enrollment.key` or `cloud.enrollment.existingSecret` with this mode. Radar
and the chart reject those conflicts instead of changing authentication modes.
The chart mounts the Secret read-only without a `subPath`. Its default pod group
can read the mounted `0440` file. If you customize the pod's user/group settings,
ensure that identity can still read the Secret volume.

For a private or self-signed Hub certificate, add its CA certificate to
`cloud.caBundle`. TLS verification cannot be disabled in this mode. If Radar
uses a different address from Hub's canonical address, an operator must allow
that exact origin in the Hub configuration and supply a certificate for it.
Use a punycode hostname for an international domain name.

Argo CD and Flux manifests should contain only the Secret reference above.
Provision the private Secret separately through your secret manager. NKP's
workspace values also need the same Secret already present in every selected
cluster. The workspace values do not create those Secrets, and private keys
must not be pasted into workspace configuration.

## Self-hosted setup before first sign-in

An operator can configure the public half as the Hub's install enrollment key.
For that configured install key only, set `cloud.enrollment.organization` to
`self-hosted`. Radar proves key ownership first; Hub makes it wait until the
singleton organization exists, then resolves that organization. Cloud and
ordinary keys created in the app require the actual organization ID.

## Rotation and revocation

Register a new public key using **Replace existing key** and confirm the
predecessor. Hub preserves each existing predecessor cluster's trust when it
rejoins; new clusters still follow the new key's policy and limits. Update the
private-key Secrets, restart the affected Radars deliberately, and verify their
cluster IDs, history and access before revoking the old enrollment key. The
agent rereads its private file each time it enrolls.

For an existing self-hosted install secret, migrate through an app-created
replacement public key while leaving the install secret configured. Changing
the Hub's install key configuration directly is a hard cutover: old agents may
be refused at their next enrollment.

Customer keys do not expire automatically. Each enrollment proof expires after
60 seconds and Hub accepts it only once. A retry obtains a fresh challenge.
Revoking an enrollment key prevents future enrollment; it does not close an
existing tunnel or revoke its issued cluster token. Use the existing cluster
controls separately for containment, and check their effect on cluster history
before removing a registration.

Use `radar cloud status` to identify public-key mode and its target organization.
For connection failures, check the Radar pod log. A missing challenge endpoint
can mean an older Hub, a disabled feature, an incomplete rollout or an incorrect
API address. Radar retries with bounded backoff and never falls back to a shared
secret or unverified TLS.
