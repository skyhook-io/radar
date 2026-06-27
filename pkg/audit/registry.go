package audit

import "github.com/skyhook-io/radar/pkg/checks"

// CheckMeta is a check's static definition (human-readable context). Aliased
// from pkg/checks so the k8s-free Checks rollup and the audit engine share one
// shape — the Hub imports only pkg/checks; the engine + registry re-export it
// here. Same fields/JSON, so radar's existing audit.CheckMeta references are
// unchanged.
type CheckMeta = checks.CheckMeta

// Reference is an authoritative link shown in the expanded check card.
type Reference = checks.Reference

// Framework constants
const (
	FrameworkNSACISA = "NSA/CISA"
	FrameworkCIS     = "CIS"
)

// Authoritative references shared across checks. The inline copy stays brief;
// these are the "go deeper" links (canonical Kubernetes docs + the hardening
// guides the Frameworks tags name) rendered in the expanded card.
var (
	refSecurityContext = Reference{Label: "K8s: Security Context", URL: "https://kubernetes.io/docs/tasks/configure-pod-container/security-context/"}
	refPodSecurityStd  = Reference{Label: "K8s: Pod Security Standards", URL: "https://kubernetes.io/docs/concepts/security/pod-security-standards/"}
	refNSACISA         = Reference{Label: "NSA/CISA Kubernetes Hardening Guide", URL: "https://media.defense.gov/2022/Aug/29/2003066362/-1/-1/0/CTR_KUBERNETES_HARDENING_GUIDANCE_1.2_20220829.PDF"}
	refResources       = Reference{Label: "K8s: Resource Management", URL: "https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/"}
	refProbes          = Reference{Label: "K8s: Liveness, Readiness & Startup Probes", URL: "https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/"}
	refImages          = Reference{Label: "K8s: Container Images", URL: "https://kubernetes.io/docs/concepts/containers/images/"}
	refDeployments     = Reference{Label: "K8s: Deployments", URL: "https://kubernetes.io/docs/concepts/workloads/controllers/deployment/"}
	refDisruptions     = Reference{Label: "K8s: Pod Disruption Budgets", URL: "https://kubernetes.io/docs/concepts/workloads/pods/disruptions/"}
	refTopologySpread  = Reference{Label: "K8s: Topology Spread Constraints", URL: "https://kubernetes.io/docs/concepts/scheduling-eviction/topology-spread-constraints/"}
	refAffinity        = Reference{Label: "K8s: Affinity and Anti-affinity", URL: "https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/#affinity-and-anti-affinity"}
	refSAToken         = Reference{Label: "K8s: Configure Service Accounts for Pods", URL: "https://kubernetes.io/docs/tasks/configure-pod-container/configure-service-account/"}
	refSecrets         = Reference{Label: "K8s: Secrets", URL: "https://kubernetes.io/docs/concepts/configuration/secret/"}
	refConfigMaps      = Reference{Label: "K8s: ConfigMaps", URL: "https://kubernetes.io/docs/concepts/configuration/configmap/"}
	refHostPath        = Reference{Label: "K8s: hostPath Volumes", URL: "https://kubernetes.io/docs/concepts/storage/volumes/#hostpath"}
	refDeprecatedAPI   = Reference{Label: "K8s: Deprecated API Migration Guide", URL: "https://kubernetes.io/docs/reference/using-api/deprecation-guide/"}
	refService         = Reference{Label: "K8s: Service", URL: "https://kubernetes.io/docs/concepts/services-networking/service/"}
	refIngress         = Reference{Label: "K8s: Ingress", URL: "https://kubernetes.io/docs/concepts/services-networking/ingress/"}
	refTraefikCRD      = Reference{Label: "Traefik: Kubernetes IngressRoute", URL: "https://doc.traefik.io/traefik/routing/providers/kubernetes-crd/"}
	refFinalizers      = Reference{Label: "K8s: Finalizers", URL: "https://kubernetes.io/docs/concepts/overview/working-with-objects/finalizers/"}
	refCrossplane      = Reference{Label: "Crossplane: Managed Resources", URL: "https://docs.crossplane.io/latest/concepts/managed-resources/"}
)

// CheckRegistry maps checkID → metadata for all built-in checks.
//
// Category MUST match the category the check emits at runtime (see pkg/audit/checks.go)
// — the settings UI groups the catalog by it, so a mismatch would file a check
// under a section its findings never appear in. TestRegistryCategoriesValid guards
// the vocabulary; the cross-resource / lifecycle / deprecated-API checks all emit
// as Reliability.
var CheckRegistry = map[string]CheckMeta{
	// ── Security ──────────────────────────────────────────────────────
	"runAsRoot": {
		ID:          "runAsRoot",
		Title:       "Run as non-root",
		Category:    CategorySecurity,
		Description: "Containers running as root have full access to the host filesystem. If compromised, an attacker gains root-level access.",
		Remediation: "Set securityContext.runAsNonRoot: true and runAsUser: 1000 (or higher) on the container or pod spec.",
		Frameworks:  []string{FrameworkNSACISA, FrameworkCIS},
		References:  []Reference{refSecurityContext, refPodSecurityStd, refNSACISA},
	},
	"privileged": {
		ID:          "privileged",
		Title:       "Privileged container",
		Category:    CategorySecurity,
		Description: "Privileged containers have all host capabilities, bypassing container isolation. An attacker in a privileged container can compromise the entire node.",
		Remediation: "Set securityContext.privileged: false on the container.",
		Frameworks:  []string{FrameworkNSACISA, FrameworkCIS},
		References:  []Reference{refSecurityContext, refPodSecurityStd, refNSACISA},
	},
	"privilegeEscalation": {
		ID:          "privilegeEscalation",
		Title:       "Privilege escalation",
		Category:    CategorySecurity,
		Description: "Allows a process to gain more privileges than its parent. Attackers can exploit this to escalate from container to host.",
		Remediation: "Set securityContext.allowPrivilegeEscalation: false on the container.",
		Frameworks:  []string{FrameworkNSACISA, FrameworkCIS},
		References:  []Reference{refSecurityContext, refPodSecurityStd},
	},
	"readOnlyRootFs": {
		ID:          "readOnlyRootFs",
		Title:       "Read-only root filesystem",
		Category:    CategorySecurity,
		Description: "A writable root filesystem allows attackers to modify binaries, install tools, or tamper with the application.",
		Remediation: "Set securityContext.readOnlyRootFilesystem: true. Use emptyDir volumes for writable paths.",
		Frameworks:  []string{FrameworkNSACISA},
		References:  []Reference{refSecurityContext, refNSACISA},
	},
	"dangerousCapabilities": {
		ID:          "dangerousCapabilities",
		Title:       "Dangerous capabilities",
		Category:    CategorySecurity,
		Description: "Capabilities like SYS_ADMIN, NET_ADMIN, and ALL grant near-root powers and can be used to escape the container.",
		Remediation: "Remove dangerous capabilities from securityContext.capabilities.add. Drop all and add only what is needed.",
		Frameworks:  []string{FrameworkNSACISA, FrameworkCIS},
		References:  []Reference{refSecurityContext, refPodSecurityStd},
	},
	"dockerSocketMount": {
		ID:          "dockerSocketMount",
		Title:       "Container runtime socket mounted",
		Category:    CategorySecurity,
		Description: "Mounting the Docker/containerd socket gives the container full control over the host's container runtime — equivalent to root access. This is a documented attack vector used in cryptojacking campaigns.",
		Remediation: "Remove the hostPath volume mounting the container runtime socket. If CI/CD needs Docker access, use Docker-in-Docker (DinD) or Kaniko instead.",
		Frameworks:  []string{FrameworkNSACISA, FrameworkCIS},
		References:  []Reference{refHostPath, refNSACISA},
	},
	"sensitiveHostPath": {
		ID:          "sensitiveHostPath",
		Title:       "Sensitive host path mounted",
		Category:    CategorySecurity,
		Description: "Mounting sensitive host directories (/etc, /proc, /sys, /var/run) exposes host configuration, credentials, and runtime state to the container. Attackers can use this to escalate privileges or access other containers.",
		Remediation: "Remove the hostPath volume or restrict it to a specific, non-sensitive directory. Use emptyDir or PVC for writable storage.",
		Frameworks:  []string{FrameworkNSACISA, FrameworkCIS},
		References:  []Reference{refHostPath, refPodSecurityStd},
	},
	"secretInConfigMap": {
		ID:          "secretInConfigMap",
		Title:       "Potential secret in ConfigMap",
		Category:    CategorySecurity,
		Description: "ConfigMap keys with names suggesting sensitive data (passwords, tokens, keys) should use Kubernetes Secrets instead. ConfigMaps are not encrypted at rest and may appear in logs and kubectl output.",
		Remediation: "Move sensitive data to a Secret resource. ConfigMaps should only contain non-sensitive configuration.",
		Frameworks:  []string{FrameworkNSACISA},
		References:  []Reference{refSecrets},
	},
	"insecureCapabilities": {
		ID:          "insecureCapabilities",
		Title:       "Insecure capabilities",
		Category:    CategorySecurity,
		Description: "Capabilities like NET_RAW, SYS_PTRACE, and MKNOD can be exploited for packet spoofing, process injection, or device manipulation.",
		Remediation: "Remove insecure capabilities from securityContext.capabilities.add. Use capabilities.drop: [ALL] and add back only what is strictly needed.",
		Frameworks:  []string{FrameworkNSACISA, FrameworkCIS},
		References:  []Reference{refSecurityContext, refPodSecurityStd},
	},
	"hostNetwork": {
		ID:          "hostNetwork",
		Title:       "Host network",
		Category:    CategorySecurity,
		Description: "Sharing the host network namespace lets the container see all network traffic and access host network services.",
		Remediation: "Remove hostNetwork: true unless the workload genuinely needs host network access.",
		Frameworks:  []string{FrameworkNSACISA, FrameworkCIS},
		References:  []Reference{refPodSecurityStd, refNSACISA},
	},
	"hostPID": {
		ID:          "hostPID",
		Title:       "Host PID namespace",
		Category:    CategorySecurity,
		Description: "Sharing the host PID namespace lets the container see and signal all host processes, enabling process injection attacks.",
		Remediation: "Remove hostPID: true from the pod spec.",
		Frameworks:  []string{FrameworkNSACISA, FrameworkCIS},
		References:  []Reference{refPodSecurityStd, refNSACISA},
	},
	"hostIPC": {
		ID:          "hostIPC",
		Title:       "Host IPC namespace",
		Category:    CategorySecurity,
		Description: "Sharing the host IPC namespace allows access to shared memory and inter-process communication with host processes.",
		Remediation: "Remove hostIPC: true from the pod spec.",
		Frameworks:  []string{FrameworkNSACISA, FrameworkCIS},
		References:  []Reference{refPodSecurityStd, refNSACISA},
	},
	"automountServiceAccountToken": {
		ID:          "automountServiceAccountToken",
		Title:       "Service account token auto-mount",
		Category:    CategorySecurity,
		Description: "Auto-mounted tokens can be used by attackers to authenticate to the Kubernetes API server and escalate privileges.",
		Remediation: "Set automountServiceAccountToken: false on the pod spec. Only enable for pods that need API access.",
		Frameworks:  []string{FrameworkNSACISA},
		References:  []Reference{refSAToken, refNSACISA},
	},

	// ── Reliability ───────────────────────────────────────────────────
	"readinessProbeMissing": {
		ID:          "readinessProbeMissing",
		Title:       "Missing readiness probe",
		Category:    CategoryReliability,
		Description: "Without a readiness probe, Kubernetes sends traffic to pods before they are ready, causing errors for users.",
		Remediation: "Add a readinessProbe (HTTP GET, TCP, or exec) to each container.",
		References:  []Reference{refProbes},
	},
	"livenessProbeMissing": {
		ID:          "livenessProbeMissing",
		Title:       "Missing liveness probe",
		Category:    CategoryReliability,
		Description: "Without a liveness probe, Kubernetes cannot detect and restart containers that are stuck or deadlocked.",
		Remediation: "Add a livenessProbe (HTTP GET, TCP, or exec) to each container.",
		References:  []Reference{refProbes},
	},
	"imageTagLatest": {
		ID:          "imageTagLatest",
		Title:       "Image tag latest or missing",
		Category:    CategoryReliability,
		Description: "Using 'latest' or no tag means deployments are not reproducible — you cannot tell which version is running, and rollbacks may not work.",
		Remediation: "Pin images to a specific version tag (e.g., nginx:1.25.3).",
		References:  []Reference{refImages},
	},
	"pullPolicyNotAlways": {
		ID:          "pullPolicyNotAlways",
		Title:       "Pull policy not Always",
		Category:    CategoryReliability,
		Description: "With a mutable tag and non-Always pull policy, nodes may run stale cached images, causing inconsistency across replicas.",
		Remediation: "Set imagePullPolicy: Always when using mutable tags, or pin to immutable digests.",
		References:  []Reference{refImages},
	},
	"singleReplica": {
		ID:          "singleReplica",
		Title:       "Single replica",
		Category:    CategoryReliability,
		Description: "A single-replica deployment has no redundancy — any pod disruption (node drain, OOM, crash) causes downtime.",
		Remediation: "Set replicas: 2 or higher, or configure an HPA for autoscaling.",
		References:  []Reference{refDeployments, refDisruptions},
	},
	"missingPDB": {
		ID:          "missingPDB",
		Title:       "Missing PodDisruptionBudget",
		Category:    CategoryReliability,
		Description: "Without a PDB, cluster operations (node drains, upgrades) can take down all replicas simultaneously.",
		Remediation: "Create a PodDisruptionBudget with minAvailable or maxUnavailable matching your availability requirements.",
		References:  []Reference{refDisruptions},
	},
	"missingTopologySpread": {
		ID:          "missingTopologySpread",
		Title:       "Missing topology spread constraints",
		Category:    CategoryReliability,
		Description: "Without topology spread constraints, all replicas can land on the same node or availability zone, giving an illusion of HA without real fault tolerance.",
		Remediation: "Add topologySpreadConstraints to the pod template with topologyKey: kubernetes.io/hostname or topology.kubernetes.io/zone.",
		References:  []Reference{refTopologySpread},
	},
	"podHARisk": {
		ID:          "podHARisk",
		Title:       "All replicas on same node",
		Category:    CategoryReliability,
		Description: "All pod replicas are scheduled on the same node. If that node fails, all replicas go down simultaneously — no actual high availability.",
		Remediation: "Add pod anti-affinity or topology spread constraints to distribute replicas across nodes.",
		References:  []Reference{refAffinity, refTopologySpread},
	},

	// ── Efficiency ────────────────────────────────────────────────────
	"cpuRequestMissing": {
		ID:          "cpuRequestMissing",
		Title:       "Missing CPU request",
		Category:    CategoryEfficiency,
		Description: "Without CPU requests, the scheduler cannot make informed placement decisions, leading to overcommitted nodes and throttling.",
		Remediation: "Set resources.requests.cpu (e.g., 100m) on each container.",
		Frameworks:  []string{FrameworkCIS},
		References:  []Reference{refResources},
	},
	"memoryRequestMissing": {
		ID:          "memoryRequestMissing",
		Title:       "Missing memory request",
		Category:    CategoryEfficiency,
		Description: "Without memory requests, pods may be scheduled on nodes without enough memory, causing OOM kills.",
		Remediation: "Set resources.requests.memory (e.g., 128Mi) on each container.",
		Frameworks:  []string{FrameworkCIS},
		References:  []Reference{refResources},
	},
	"cpuLimitMissing": {
		ID:          "cpuLimitMissing",
		Title:       "Missing CPU limit",
		Category:    CategoryEfficiency,
		Description: "Without CPU limits, a single container can consume all CPU on a node, starving other workloads.",
		Remediation: "Set resources.limits.cpu (e.g., 500m) on each container.",
		Frameworks:  []string{FrameworkCIS},
		References:  []Reference{refResources},
	},
	"memoryLimitMissing": {
		ID:          "memoryLimitMissing",
		Title:       "Missing memory limit",
		Category:    CategoryEfficiency,
		Description: "Without memory limits, a container can consume unbounded memory, eventually triggering kernel OOM kills on the node.",
		Remediation: "Set resources.limits.memory (e.g., 512Mi) on each container.",
		Frameworks:  []string{FrameworkCIS},
		References:  []Reference{refResources},
	},
	"resourceUtilization": {
		ID:          "resourceUtilization",
		Title:       "Resource utilization mismatch",
		Category:    CategoryEfficiency,
		Description: "Pod resource usage is significantly different from its requests — either wasting resources (<10% used) or at risk of throttling/OOM (>90% used).",
		Remediation: "Adjust resources.requests to match actual usage. Use metrics or VPA recommendations to right-size.",
		References:  []Reference{refResources},
	},
	"orphanConfigMapSecret": {
		ID:          "orphanConfigMapSecret",
		Title:       "Unused ConfigMap or Secret",
		Category:    CategoryEfficiency,
		Description: "This ConfigMap or Secret is not referenced by any pod (env vars, volumes, or imagePullSecrets). It may be orphaned and safe to remove.",
		Remediation: "Verify this resource is no longer needed and delete it, or add a reference from a pod spec if it should be in use.",
		References:  []Reference{refConfigMaps, refSecrets},
	},

	// ── Cross-resource / lifecycle (emit under Reliability) ────────────
	"deprecatedAPIVersion": {
		ID:          "deprecatedAPIVersion",
		BadgeWorthy: true,
		Title:       "Deprecated API version",
		Category:    CategoryReliability,
		Description: "This cluster still serves a deprecated API version. Resources using this API will break after a Kubernetes upgrade that removes it.",
		Remediation: "Migrate resources to the replacement API version before upgrading the cluster.",
		References:  []Reference{refDeprecatedAPI},
	},
	"serviceNoMatchingPods": {
		ID:          "serviceNoMatchingPods",
		BadgeWorthy: true,
		Title:       "Service has no matching pods",
		Category:    CategoryReliability,
		Description: "The service selector does not match any running pods — traffic sent to this service will fail.",
		Remediation: "Verify the service selector labels match the pod template labels on the target workload.",
		References:  []Reference{refService},
	},
	"ingressNoMatchingService": {
		ID:          "ingressNoMatchingService",
		BadgeWorthy: true,
		Title:       "Ingress references missing service",
		Category:    CategoryReliability,
		Description: "The ingress backend references a service that does not exist, so incoming traffic will get 503 errors.",
		Remediation: "Check the ingress spec and correct the service name, or create the missing service.",
		References:  []Reference{refIngress},
	},
	"traefikRouteMissingService": {
		ID:          "traefikRouteMissingService",
		BadgeWorthy: true,
		Title:       "Traefik router references missing service",
		Category:    CategoryReliability,
		Description: "A Traefik IngressRoute references a Service or TraefikService that does not exist. Traefik ships no admission webhook, so the bad reference is accepted silently and requests to that route fail until someone reads the controller logs.",
		Remediation: "Correct the service name/namespace in the router's spec.routes[].services, or create the missing Service/TraefikService. Cross-namespace references also require allowCrossNamespace on the Traefik CRD provider.",
		References:  []Reference{refTraefikCRD},
	},
	"traefikRouteMissingMiddleware": {
		ID:          "traefikRouteMissingMiddleware",
		BadgeWorthy: true,
		Title:       "Traefik router references missing middleware",
		Category:    CategoryReliability,
		Description: "A Traefik IngressRoute references a Middleware that does not exist in the resolved namespace. Traefik accepts the route but skips the missing middleware, so auth, rate-limit, or header rules you expect to apply silently do not.",
		Remediation: "Correct the middleware name/namespace in the router's spec.routes[].middlewares, or create the missing Middleware. A same-name Middleware in a different API group (traefik.io vs traefik.containo.us) does not satisfy the reference.",
		References:  []Reference{refTraefikCRD},
	},
	"traefikChainMissingMiddleware": {
		ID:          "traefikChainMissingMiddleware",
		BadgeWorthy: true,
		Title:       "Traefik chain references missing middleware",
		Category:    CategoryReliability,
		Description: "A Traefik chain Middleware lists a Middleware in spec.chain.middlewares that does not exist in the resolved namespace. Traefik accepts the chain but skips the missing link, so part of the intended middleware pipeline silently does not run.",
		Remediation: "Correct the middleware name/namespace in spec.chain.middlewares, or create the missing Middleware. A same-name Middleware in a different API group (traefik.io vs traefik.containo.us) does not satisfy the reference.",
		References:  []Reference{refTraefikCRD},
	},
	"traefikErrorsMissingService": {
		ID:          "traefikErrorsMissingService",
		BadgeWorthy: true,
		Title:       "Traefik errors middleware references missing service",
		Category:    CategoryReliability,
		Description: "A Traefik errors Middleware references a Service in spec.errors.service that does not exist. Traefik accepts the middleware, but when an error page is needed the backend isn't there, so users get the default error instead of the custom one.",
		Remediation: "Correct the service name/namespace in spec.errors.service, or create the missing Service. Cross-namespace references also require allowCrossNamespace on the Traefik CRD provider.",
		References:  []Reference{refTraefikCRD},
	},
	"stuckTerminating": {
		ID: "stuckTerminating",
		// Not BadgeWorthy: stuck-terminating is already surfaced by the live health
		// pipeline (pkg/health renders the node degraded) and the "Terminating" chip
		// in the resource-list name cell — a badge here would be a third redundant
		// signal. It stays in the Checks/Audit views with its deletion-age ramp.
		Title: "Stuck terminating resource",
		Category:    CategoryReliability,
		Description: "This resource has metadata.deletionTimestamp set but is still alive past the cleanup window. Most controllers finish cleanup within seconds; minutes-long delays usually mean a finalizer's owning controller is unhealthy or unable to reach a dependent service. Common causes: the controller pod is CrashLoopBackOff, DNS resolution is broken, the finalizer logic depends on a webhook or external API that's unavailable.",
		Remediation: "Check the controller responsible for each finalizer key (kubectl describe will show finalizers under metadata). For Argo CD, look at argocd-application-controller in the argocd namespace. For Flux, look at the matching controller (kustomize-controller, helm-controller, source-controller) in flux-system. Once the controller is healthy, deletion will resume automatically. If you must remove a stuck resource and accept the orphaned cleanup, manually clear the finalizers field — but only as a last resort.",
		References:  []Reference{refFinalizers},
	},
	"crossplaneStuck": {
		ID:          "crossplaneStuck",
		BadgeWorthy: true,
		Title:       "Stuck Crossplane resource",
		Category:    CategoryReliability,
		Description: "A Crossplane Managed Resource, Composite Resource, or Claim has been reporting Ready=False or Synced=False past the reconciliation window. Synced=False usually means a configuration error (bad ProviderConfig, malformed forProvider spec, missing IAM permissions, quota exceeded, schema mismatch). Ready=False usually means the provider accepted the spec but can't reach a desired state (target cloud API rejected, dependency missing, eventual-consistency lag past the threshold).",
		Remediation: "Open the resource and read the latest condition's message — Crossplane providers almost always include the upstream cloud error verbatim. Common fixes: (1) check the linked Provider's Healthy condition; the package controller crashlooping starves every MR it manages. (2) verify the ProviderConfig credentials Secret still exists and is current (rotated keys, expired tokens). (3) check IAM/RBAC at the target cloud — Synced=False with auth errors needs a credentials fix, not a Crossplane fix. (4) look at quota/limit errors in the cloud provider console. (5) for paused resources (crossplane.io/paused annotation), this finding is intentionally suppressed — remove the annotation to resume reconciliation.",
		References:  []Reference{refCrossplane},
	},
}

// The two Checks-ladder levels a detector reaches with no override —
// pkg/checks.MapSeverity emits only these (critical/low need an org override).
var (
	sevHigh   = string(checks.SeverityHigh)
	sevMedium = string(checks.SeverityMedium)
)

// nonMediumDefault lists the checks shown as High; everything else defaults to
// Medium. Mirror pkg/audit/checks.go emission when adding or re-ranking a check
// (TestRegistryDefaultSeverity guards the values).
var nonMediumDefault = map[string]string{
	// Always emit danger → High.
	"privileged":            sevHigh,
	"privilegeEscalation":   sevHigh,
	"dangerousCapabilities": sevHigh,
	"dockerSocketMount":     sevHigh,
	"hostPID":               sevHigh,
	"hostIPC":               sevHigh,
	"imageTagLatest":        sevHigh,
	"deprecatedAPIVersion":  sevHigh,
	// Context-dependent — emit warning OR danger (sensitiveHostPath: a full
	// host-root mount is danger, sensitive prefixes warning; stuck*/crossplane
	// ramp warning→danger past ~30min). Surfaced as High (their worst case) so
	// the settings catalog doesn't UNDER-call them — under-calling severity is
	// the dangerous error on a posture surface. The queue still shows each
	// finding's exact severity.
	"sensitiveHostPath": sevHigh,
	"stuckTerminating":  sevHigh,
	"crossplaneStuck":   sevHigh,
}

// Stamp DefaultSeverity onto the catalog so the Hub's Checks settings can show
// the level a check resolves to (vs. just "Default") without each consumer
// re-deriving it from raw findings.
func init() {
	for id := range CheckRegistry {
		sev := sevMedium
		if s, ok := nonMediumDefault[id]; ok {
			sev = s
		}
		m := CheckRegistry[id]
		m.DefaultSeverity = sev
		CheckRegistry[id] = m
	}
}
