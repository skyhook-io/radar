# Security Policy

## Reporting a Vulnerability

**Please do not report security vulnerabilities through public GitHub issues.**

Instead, please report them via email to security@skyhook.io.

Include as much of the following information as possible:

- Type of issue (e.g., privilege escalation, information disclosure)
- Full paths of source file(s) related to the issue
- Location of the affected source code (tag/branch/commit or direct URL)
- Step-by-step instructions to reproduce the issue
- Proof-of-concept or exploit code (if possible)
- Impact of the issue, including how an attacker might exploit it

## Response Timeline

- **Initial response**: Within 48 hours
- **Status update**: Within 7 days
- **Fix timeline**: Depends on severity, typically within 30-90 days

## Supported Versions

| Version | Supported          |
| ------- | ------------------ |
| Latest release | :white_check_mark: |
| Previous releases | Best effort |

## Security Model

### Local Execution (Default)

When running Radar locally on your machine:

- **Uses your kubeconfig**: Radar authenticates using your existing `~/.kube/config` credentials
- **Your permissions apply**: All operations are subject to your Kubernetes RBAC permissions
- **No external communication**: Radar only communicates with the Kubernetes API server specified in your kubeconfig
- **No persistent storage**: By default, no data persists between sessions (optional SQLite timeline storage is local-only)

### In-Cluster Deployment

When deploying Radar inside a Kubernetes cluster:

- **ServiceAccount-based auth**: Uses the pod's ServiceAccount for Kubernetes API access
- **RBAC-scoped permissions**: Configure the ServiceAccount with minimal required permissions
- **Team access**: Expose via Ingress with your organization's authentication (OAuth, SSO, etc.)
- **Advanced permission management**: Coming soon - granular user/role management for shared deployments

### Capabilities

Radar provides both read and write operations:

**Read Operations:**
- Browse all Kubernetes resources (Pods, Deployments, Services, etc.)
- View resource YAML manifests and relationships
- Stream pod logs
- View Kubernetes events and resource change history
- List Helm releases and their configurations

**Write Operations:**
- Edit and update resource manifests (YAML)
- Delete resources
- Restart workloads (Deployments, StatefulSets, DaemonSets)
- Trigger, suspend, and resume CronJobs
- Exec into pod containers (terminal access)
- Create and manage port forwards
- Helm operations: upgrade, rollback, uninstall releases
- Switch kubectl contexts

All write operations require appropriate RBAC permissions. If your kubeconfig or ServiceAccount lacks permission for an operation, it will fail with an authorization error.

## Best Practices

### For Local Use

1. Use a kubeconfig with the minimum permissions you need
2. Consider using read-only ServiceAccounts for browsing production clusters
3. Don't expose the Radar port to the network when running locally
4. Keep Radar updated to the latest version

### For In-Cluster Deployment

1. Create a dedicated ServiceAccount with scoped RBAC permissions
2. Use read-only ClusterRole bindings for view-only deployments
3. Always deploy behind authentication (OAuth2 Proxy, Ingress auth, etc.)
4. Use NetworkPolicies to restrict access to the Radar pod
5. Regularly audit ServiceAccount permissions
