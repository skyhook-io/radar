# Authentication & Authorization

Radar supports optional user authentication with per-user authorization powered by Kubernetes RBAC. When enabled, each user sees only the namespaces they have access to, and write operations (restart, scale, exec, Helm, etc.) are executed with the user's identity — so K8s RBAC controls what each user can do.

> **No auth by default.** When running locally or without `--auth-mode`, everything works exactly as before — no login, no restrictions.

## How It Works

```
User → [Auth Layer] → Radar Backend → K8s API (as user, via impersonation)
```

1. **Authentication** identifies the user (proxy headers or OIDC login)
2. **Reads** are filtered — Radar discovers which namespaces the user can access via `SubjectAccessReview` and only returns resources from those namespaces
3. **Writes** use K8s impersonation — Radar makes the K8s API call as the authenticated user, so K8s RBAC decides whether it's allowed
4. **UI adapts** — capability checks run per-user, so buttons (exec, restart, scale, Helm) only appear if the user has permission

Radar doesn't have its own role/permission system. It delegates everything to K8s RBAC, which means permissions are managed with standard K8s tooling (`kubectl`, Terraform, GitOps, etc.).

## Auth Modes

| Mode | Flag | When to Use |
|------|------|-------------|
| `none` | `--auth-mode=none` | Local use, single-user, no restrictions needed (default) |
| `proxy` | `--auth-mode=proxy` | You already have an auth proxy (oauth2-proxy, Pomerium, Cloudflare Access, etc.) in front of your ingress |
| `oidc` | `--auth-mode=oidc` | You want Radar to handle login directly — no separate auth proxy needed |

**How to choose:** If your organization already routes internal tools through an auth proxy or SSO gateway, use **proxy** mode — it's zero config on the Radar side. If you want a standalone deployment with built-in login, use **OIDC** mode.

### Proxy Mode

Use this when you already have (or plan to deploy) an auth proxy like [oauth2-proxy](https://oauth2-proxy.github.io/oauth2-proxy/), Pomerium, Authelia, or similar. The proxy authenticates users and forwards their identity to Radar via HTTP headers.

**Flow:**
```
Browser → Auth Proxy → Radar
           sets X-Forwarded-User: alice@company.com
           sets X-Forwarded-Groups: sre-team,platform-eng
```

**Helm values:**
```yaml
auth:
  mode: proxy
  # Optional: customize header names (these are the defaults)
  # proxy:
  #   userHeader: X-Forwarded-User
  #   groupsHeader: X-Forwarded-Groups
```

**CLI flags:**
```bash
radar --auth-mode=proxy \
      --auth-user-header=X-Forwarded-User \
      --auth-groups-header=X-Forwarded-Groups
```

> **Security:** Your ingress must strip `X-Forwarded-User` and `X-Forwarded-Groups` headers from external requests to prevent spoofing. The auth proxy should be the **only** path to Radar. Radar logs a warning at startup as a reminder.

### OIDC Mode

Use this when you want Radar to handle login directly — no separate auth proxy needed. Radar redirects to your identity provider (Google, Okta, Dex, Keycloak, etc.), validates the token, and creates a session cookie.

**Flow:**
```
Browser → Radar → redirects to IdP → user logs in → callback → session cookie
```

**Helm values:**
```yaml
auth:
  mode: oidc
  secret: ""  # HMAC key for session cookies (auto-generated if empty, but sessions won't survive pod restarts)
  oidc:
    issuerURL: https://accounts.google.com    # Your OIDC provider
    clientID: your-client-id
    clientSecret: your-client-secret
    redirectURL: https://radar.example.com/auth/callback
    groupsClaim: groups                        # JWT claim containing group membership
```

**Logout behavior:**

When a user clicks logout, Radar clears the local session cookie and — if the identity provider supports it — redirects the browser to the provider's logout endpoint ([RP-Initiated Logout](https://openid.net/specs/openid-connect-rpinitiated-1_0.html)) to terminate the SSO session as well. This prevents the common issue where the user appears to log out but is silently re-authenticated on the next visit.

Radar discovers the provider's `end_session_endpoint` automatically from the OIDC discovery document at startup. If the provider supports it (Okta, Keycloak, Azure AD), Radar redirects the browser there to terminate the SSO session. If the provider doesn't advertise `end_session_endpoint` (e.g., Google), Radar uses `prompt=login` on the next authorization request to force the IdP to show a login screen instead of silently re-authenticating. Check the startup logs for confirmation:

```
[oidc] RP-Initiated Logout enabled (end_session_endpoint discovered)
# or
[oidc] IdP does not advertise end_session_endpoint — will use prompt=login on next auth after logout
```

To redirect users back to Radar after IdP logout, set `--auth-oidc-post-logout-redirect-url` (or `auth.oidc.postLogoutRedirectURL` in Helm). This URL **must be registered** with your identity provider as a valid post-logout redirect URI.

**Back-Channel Logout (IdP-initiated session revocation):**

When an admin disables a user at the IdP level (e.g., disables an Okta account), Radar has no way to know — the existing session cookie remains valid until it expires. Back-Channel Logout ([spec](https://openid.net/specs/openid-connect-backchannel-1_0.html)) solves this: the IdP POSTs a signed `logout_token` to Radar's `/auth/backchannel-logout` endpoint, and Radar immediately revokes the matching session.

To enable, set `--auth-oidc-backchannel-logout` (or `auth.oidc.backchannelLogout: true` in Helm), then register `https://radar.example.com/auth/backchannel-logout` as the Back-Channel Logout URI in your IdP.

```yaml
auth:
  mode: oidc
  oidc:
    issuerURL: https://your-idp.example.com
    clientID: radar
    backchannelLogout: true
```

Check the startup logs to confirm IdP support:
```
[oidc] Backchannel Logout enabled (sid-based revocation)
# or
[oidc] WARNING: --auth-oidc-backchannel-logout is set, but IdP does not advertise backchannel_logout_supported.
```

**Limitations:**
- **Single-replica only.** Revocations are stored in memory and not shared across pods. If Radar runs multiple replicas behind a load balancer, a revocation hitting one pod won't affect sessions on other pods.
- **Lost on restart.** If Radar restarts, all pending revocations are lost. The session will expire naturally at cookie TTL (4h default).
- **`sid` required for targeted revocation.** If the IdP's logout_token contains only `sub` (no `sid`), Radar cannot target the specific session — it logs a warning and the session expires at cookie TTL. Most major IdPs (Okta, Keycloak, Auth0, Azure AD) include `sid`.
- **Google does not support back-channel logout.** For Google, session exposure is bounded by cookie TTL only.

**Using K8s Secrets for sensitive values:**

For production, use K8s Secrets instead of storing credentials in Helm values (which are visible in Helm release history):

```yaml
auth:
  mode: oidc
  # Session signing key from a K8s Secret
  existingSecret: radar-auth-secret        # K8s Secret containing the HMAC key
  existingSecretKey: auth-secret            # Key within the Secret (default)
  oidc:
    issuerURL: https://accounts.google.com
    clientID: your-client-id
    # OIDC client secret from a K8s Secret
    existingSecret: radar-oidc-credentials  # K8s Secret containing the client secret
    clientSecretKey: client-secret           # Key within the Secret (default)
    redirectURL: https://radar.example.com/auth/callback
```

You can also mix approaches — use `existingSecret` for the client secret but pass `clientID` as a plain value (it's not sensitive).

**TLS configuration for self-signed certificates:**

If your OIDC provider uses a self-signed or internal CA certificate (e.g., on-prem Keycloak), use one of:

```yaml
auth:
  oidc:
    caCert: /etc/radar/oidc-ca.crt          # Path to CA certificate file (secure)
    # insecureSkipVerify: true              # Skip TLS verification (dev/test only)
```

When using `caCert` in Kubernetes, mount the CA certificate into the pod via a ConfigMap or Secret volume.

### Radar Cloud mode

If you see `RADAR_CLOUD_MODE` or `cloud.*` values in the chart, they control a specialized deployment mode used by [Radar Cloud](https://radarhq.io) — a hosted SaaS that lets a single Cloud frontend manage many in-cluster Radar instances over an outbound tunnel. You don't need to use it to run Radar standalone; leave `cloud.enabled: false` (the default).

Under cloud-mode (`RADAR_CLOUD_MODE=true`, set automatically by the chart when `cloud.enabled=true`), Radar:

- Forces `--auth-mode=proxy` with pinned `X-Forwarded-User` / `X-Forwarded-Groups` headers — the Cloud tunnel is the trust boundary.
- Ships three default ClusterRoleBindings mapping Cloud's `cloud:owner` / `cloud:member` / `cloud:viewer` groups to the standard K8s `admin` / `edit` / `view` ClusterRoles. Configurable via `cloud.defaultRbac.*` in `values.yaml`.
- Hardens the listener (no `/debug/pprof/*`, narrower exempt paths).

Customer-facing documentation for Radar Cloud lives on [radarhq.io](https://radarhq.io). The authoritative reference for the Cloud-mode chart values is the comment block in [`deploy/helm/radar/values.yaml`](../deploy/helm/radar/values.yaml) under `cloud:`.

## Setting Up User Permissions

Radar delegates authorization entirely to K8s RBAC via impersonation. It doesn't have its own role system — permissions are managed with standard K8s tooling (kubectl, Terraform, Helm, GitOps, etc.).

**If your cluster already has RBAC bindings** (most production clusters do), they work automatically. Radar impersonates the authenticated user, and K8s evaluates their existing bindings. You only need to create new bindings if users don't already have the access they need.

### How It Works with Cloud Providers

Cloud-managed clusters typically map their IAM to K8s RBAC automatically:

- **GKE**: Google Groups for RBAC maps Google Workspace groups to K8s groups. IAM roles (`roles/container.viewer`, etc.) grant K8s access. The username is the user's Google email.
- **EKS**: The `aws-auth` ConfigMap maps IAM roles/users to K8s users and groups. The username is typically the IAM role ARN or a mapped alias.
- **AKS**: Azure AD integration maps Azure AD groups directly to K8s groups. The username is the Azure AD user principal name.

In all cases, the username and groups that Radar receives from your auth layer (proxy headers or OIDC token) must match what K8s RBAC expects. Usually this means using the same email/UPN your cloud provider uses.

### Understanding the Chain

```
Identity Provider (Google, Okta, etc.)
  → returns: username=alice@company.com, groups=[sre-team]

Auth Layer (proxy headers or OIDC token)
  → Radar extracts: user="alice@company.com", groups=["sre-team"]

Radar backend
  → creates impersonated K8s client: act as alice@company.com in group sre-team

K8s API server
  → checks RBAC: does any RoleBinding/ClusterRoleBinding grant "sre-team" this permission?
  → allow or deny
```

K8s RBAC doesn't require users to have ServiceAccounts. It supports binding roles to `User` (a string like `alice@company.com`) and `Group` (a string like `sre-team`). These strings just need to match what the identity provider returns.

### Using Built-in K8s Roles

K8s ships with built-in ClusterRoles that work well with Radar. You don't need to create custom roles unless you want fine-grained control:

| Built-in Role | What Users Can Do in Radar |
|---------------|---------------------------|
| `view` | See resources, topology, events, logs. No restart, scale, or Helm writes. |
| `edit` | Everything in `view` + restart, scale, edit resources. Does NOT include exec or port-forward (those require `pods/exec` and `pods/portforward` subresources — use the custom `radar-operator` role below). |
| `admin` | Everything in `edit` + manage RBAC within namespaces. Same exec/port-forward caveat as `edit`. |
| `cluster-admin` | Full access to everything across all namespaces, including exec and port-forward. |

```yaml
# Example: give the dev-team group read-only access to all namespaces
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: dev-team-view
subjects:
  - kind: Group
    name: dev-team
    apiGroup: rbac.authorization.k8s.io
roleRef:
  kind: ClusterRole
  name: view
  apiGroup: rbac.authorization.k8s.io
```

For more control, create a custom ClusterRole:

### Step 1: Create a ClusterRole with the permissions you want

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: radar-operator
rules:
  # View workloads (most users)
  - apiGroups: ["", "apps", "batch"]
    resources: ["pods", "deployments", "statefulsets", "daemonsets", "services",
                "configmaps", "jobs", "cronjobs", "replicasets", "events"]
    verbs: ["get", "list", "watch"]

  # Restart and scale workloads
  - apiGroups: ["apps"]
    resources: ["deployments", "statefulsets", "daemonsets"]
    verbs: ["patch", "update"]

  # View logs
  - apiGroups: [""]
    resources: ["pods/log"]
    verbs: ["get"]

  # Exec into pods (optional — omit for read-only users)
  - apiGroups: [""]
    resources: ["pods/exec"]
    verbs: ["create"]
```

### Step 2: Bind it to your users or groups

**By group** (recommended — matches group from your identity provider):
```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: sre-team-radar-operator
subjects:
  - kind: Group
    name: sre-team                    # Must match the group string from IdP
    apiGroup: rbac.authorization.k8s.io
roleRef:
  kind: ClusterRole
  name: radar-operator
  apiGroup: rbac.authorization.k8s.io
```

**By user:**
```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: alice-radar-operator
subjects:
  - kind: User
    name: alice@company.com           # Must match the username from IdP
    apiGroup: rbac.authorization.k8s.io
roleRef:
  kind: ClusterRole
  name: radar-operator
  apiGroup: rbac.authorization.k8s.io
```

**Per-namespace** (use RoleBinding instead of ClusterRoleBinding):
```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: dev-team-radar-operator
  namespace: staging                  # Only grants access in this namespace
subjects:
  - kind: Group
    name: dev-team
    apiGroup: rbac.authorization.k8s.io
roleRef:
  kind: ClusterRole
  name: radar-operator
  apiGroup: rbac.authorization.k8s.io
```

### Step 3: Verify

After deploying with auth enabled, you can verify impersonation works:

```bash
# Check what alice can do (from a machine with cluster-admin access)
kubectl auth can-i --list --as=alice@company.com --as-group=sre-team

# Check specific permission
kubectl auth can-i get pods -n production --as=alice@company.com --as-group=sre-team
```

## What Users See

When auth is enabled:
- A **username** appears in the Radar header with a logout option
- The **namespace selector** only shows namespaces the user can access
- **Topology, resources, events, dashboard** are filtered to accessible namespaces
- **Cluster-scoped resources** (Nodes, PersistentVolumes, StorageClasses) are currently visible to all authenticated users regardless of namespace permissions — per-resource SAR checks for these are planned for a future release
- **Helm releases** are visible to all authenticated users (reads use the ServiceAccount, not impersonation, because the K8s `view` role doesn't include `list secrets` which Helm requires). Write operations (install, upgrade, rollback, uninstall) are impersonated and require the user to have appropriate RBAC.
- **Write buttons** (restart, scale, exec, Helm install, etc.) only appear if the user has permission
- Write operations return **403** from K8s if RBAC denies them (shown as an error toast)
- The **/api/auth/me** endpoint returns the current user info and whether auth is enabled

## ServiceAccount RBAC

When auth is enabled, Radar's ServiceAccount needs two additional permissions (added automatically by the Helm chart):

```yaml
# Impersonate users and groups
- apiGroups: [""]
  resources: ["users", "groups"]
  verbs: ["impersonate"]

# Check permissions for specific users
- apiGroups: ["authorization.k8s.io"]
  resources: ["subjectaccessreviews"]
  verbs: ["create"]
```

The ServiceAccount's existing read permissions (list pods, watch deployments, etc.) continue to power the shared cache. Impersonation is only used for write operations and permission checks.

## Session Cookies

Radar uses stateless HMAC-SHA256 signed cookies for sessions. The cookie contains the username and groups — no server-side session storage.

- **Cookie TTL**: 4 hours by default (sliding), configurable with `--auth-cookie-ttl` or `auth.cookieTTL` in Helm values. Sessions auto-extend while you're active; idle sessions expire after the configured TTL. Active users won't notice — Radar's frontend polling keeps the session alive automatically.
- **Proxy mode**: When the cookie expires, the middleware transparently re-creates the session from proxy headers on the next request, so the shorter default TTL has no UX impact.
- **Session secret**: Set `auth.secret` or `RADAR_AUTH_SECRET` env var. If empty, a random key is generated at startup (sessions won't survive pod restarts)
- **For production**: Use `auth.existingSecret` to reference a K8s Secret, so sessions survive restarts

## Configuration Reference

| Parameter | CLI Flag | Helm Value | Default |
|-----------|----------|------------|---------|
| Auth mode | `--auth-mode` | `auth.mode` | `none` |
| Session secret | `--auth-secret` | `auth.secret` | auto-generated |
| Cookie TTL | `--auth-cookie-ttl` | `auth.cookieTTL` | `4h` (sliding) |
| User header (proxy) | `--auth-user-header` | `auth.proxy.userHeader` | `X-Forwarded-User` |
| Groups header (proxy) | `--auth-groups-header` | `auth.proxy.groupsHeader` | `X-Forwarded-Groups` |
| OIDC issuer | `--auth-oidc-issuer` | `auth.oidc.issuerURL` | — |
| OIDC client ID | `--auth-oidc-client-id` | `auth.oidc.clientID` | — |
| OIDC client secret | `--auth-oidc-client-secret` | `auth.oidc.clientSecret` | — |
| OIDC client secret (K8s Secret) | — | `auth.oidc.existingSecret` | — |
| OIDC client secret key | — | `auth.oidc.clientSecretKey` | `client-secret` |
| OIDC redirect URL | `--auth-oidc-redirect-url` | `auth.oidc.redirectURL` | — |
| OIDC groups claim | `--auth-oidc-groups-claim` | `auth.oidc.groupsClaim` | `groups` |
| OIDC post-logout redirect | `--auth-oidc-post-logout-redirect-url` | `auth.oidc.postLogoutRedirectURL` | — |
| OIDC username prefix | `--auth-oidc-username-prefix` | `auth.oidc.usernamePrefix` | — |
| OIDC groups prefix | `--auth-oidc-groups-prefix` | `auth.oidc.groupsPrefix` | — |
| OIDC CA certificate | `--auth-oidc-ca-cert` | `auth.oidc.caCert` | — |
| OIDC skip TLS verify | `--auth-oidc-insecure-skip-verify` | `auth.oidc.insecureSkipVerify` | `false` |
| OIDC backchannel logout | `--auth-oidc-backchannel-logout` | `auth.oidc.backchannelLogout` | `false` |

## Troubleshooting

### User is logged back in immediately after logout (OIDC)

This happens when the identity provider's SSO session is not terminated during logout. Check:

1. **Check the startup logs.** Look for `[oidc] RP-Initiated Logout enabled` or the `prompt=login` fallback message. Both approaches should prevent silent re-authentication.
2. **Is `post_logout_redirect_uri` registered?** If you set `--auth-oidc-post-logout-redirect-url`, it must be registered as a valid post-logout redirect URI with your identity provider. If not registered, the IdP may show an error instead of redirecting.
3. **Browser extensions or aggressive caching?** Some browser extensions may interfere with the redirect to the IdP's logout endpoint.

### Users get 401 on every request

- **Proxy mode**: Check that the auth proxy is setting `X-Forwarded-User`. Inspect with:
  ```bash
  kubectl logs -n radar -l app.kubernetes.io/name=radar | grep "auth"
  ```
- **OIDC mode**: Verify the issuer URL, client ID, and redirect URL are correct. Check Radar logs for OIDC errors.

### Users authenticate but see empty dashboard / no resources

The user will see a "No Namespace Access" message if they have no K8s RBAC bindings. Radar uses `SubjectAccessReview` to discover which namespaces each user can access — with no bindings, the result is zero namespaces.

**Verify the user's access:**
```bash
# Check if the user can list pods in a specific namespace
kubectl auth can-i list pods -n <namespace> \
  --as=<username> --as-group=<group>

# Check cluster-wide access
kubectl auth can-i list pods --all-namespaces \
  --as=<username> --as-group=<group>
```

**Fix:** Create a RoleBinding or ClusterRoleBinding for the user/group (see examples above).

### All users see empty data even with correct RBAC

Radar's ServiceAccount may be missing the `subjectaccessreviews` permission needed to check user access. When this fails, Radar denies all namespace access (fail-closed). Verify:
```bash
kubectl auth can-i create subjectaccessreviews \
  --as=system:serviceaccount:radar:radar
```

If this returns `no`, the Helm chart's RBAC was not applied. Re-install with `rbac.create: true` (the default) or add the rule manually.

### Write operations return 403

The user's K8s RBAC doesn't include the required verb. Check with:
```bash
kubectl auth can-i patch deployments -n <namespace> \
  --as=<username> --as-group=<group>
```

### Impersonation errors in Radar logs

Radar's ServiceAccount is missing impersonation permissions. Verify:
```bash
kubectl auth can-i impersonate users \
  --as=system:serviceaccount:radar:radar
```

If using `rbac.create: false` in Helm, make sure your custom ClusterRole includes the impersonation rules.
