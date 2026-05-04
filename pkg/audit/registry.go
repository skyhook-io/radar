package audit

// CheckMeta provides human-readable context for a best-practice check.
type CheckMeta struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Remediation string   `json:"remediation"`
	Frameworks  []string `json:"frameworks,omitempty"`
}

// Framework constants
const (
	FrameworkNSACISA = "NSA/CISA"
	FrameworkCIS     = "CIS"
)

// CheckRegistry maps checkID → metadata for all built-in checks.
var CheckRegistry = map[string]CheckMeta{
	// ── Security ──────────────────────────────────────────────────────
	"runAsRoot": {
		ID:          "runAsRoot",
		Title:       "Run as non-root",
		Description: "Containers running as root have full access to the host filesystem. If compromised, an attacker gains root-level access.",
		Remediation: "Set securityContext.runAsNonRoot: true and runAsUser: 1000 (or higher) on the container or pod spec.",
		Frameworks:  []string{FrameworkNSACISA, FrameworkCIS},
	},
	"privileged": {
		ID:          "privileged",
		Title:       "Privileged container",
		Description: "Privileged containers have all host capabilities, bypassing container isolation. An attacker in a privileged container can compromise the entire node.",
		Remediation: "Set securityContext.privileged: false on the container.",
		Frameworks:  []string{FrameworkNSACISA, FrameworkCIS},
	},
	"privilegeEscalation": {
		ID:          "privilegeEscalation",
		Title:       "Privilege escalation",
		Description: "Allows a process to gain more privileges than its parent. Attackers can exploit this to escalate from container to host.",
		Remediation: "Set securityContext.allowPrivilegeEscalation: false on the container.",
		Frameworks:  []string{FrameworkNSACISA, FrameworkCIS},
	},
	"readOnlyRootFs": {
		ID:          "readOnlyRootFs",
		Title:       "Read-only root filesystem",
		Description: "A writable root filesystem allows attackers to modify binaries, install tools, or tamper with the application.",
		Remediation: "Set securityContext.readOnlyRootFilesystem: true. Use emptyDir volumes for writable paths.",
		Frameworks:  []string{FrameworkNSACISA},
	},
	"dangerousCapabilities": {
		ID:          "dangerousCapabilities",
		Title:       "Dangerous capabilities",
		Description: "Capabilities like SYS_ADMIN, NET_ADMIN, and ALL grant near-root powers and can be used to escape the container.",
		Remediation: "Remove dangerous capabilities from securityContext.capabilities.add. Drop all and add only what is needed.",
		Frameworks:  []string{FrameworkNSACISA, FrameworkCIS},
	},
	"dockerSocketMount": {
		ID:          "dockerSocketMount",
		Title:       "Container runtime socket mounted",
		Description: "Mounting the Docker/containerd socket gives the container full control over the host's container runtime — equivalent to root access. This is a documented attack vector used in cryptojacking campaigns.",
		Remediation: "Remove the hostPath volume mounting the container runtime socket. If CI/CD needs Docker access, use Docker-in-Docker (DinD) or Kaniko instead.",
		Frameworks:  []string{FrameworkNSACISA, FrameworkCIS},
	},
	"sensitiveHostPath": {
		ID:          "sensitiveHostPath",
		Title:       "Sensitive host path mounted",
		Description: "Mounting sensitive host directories (/etc, /proc, /sys, /var/run) exposes host configuration, credentials, and runtime state to the container. Attackers can use this to escalate privileges or access other containers.",
		Remediation: "Remove the hostPath volume or restrict it to a specific, non-sensitive directory. Use emptyDir or PVC for writable storage.",
		Frameworks:  []string{FrameworkNSACISA, FrameworkCIS},
	},
	"secretInConfigMap": {
		ID:          "secretInConfigMap",
		Title:       "Potential secret in ConfigMap",
		Description: "ConfigMap keys with names suggesting sensitive data (passwords, tokens, keys) should use Kubernetes Secrets instead. ConfigMaps are not encrypted at rest and may appear in logs and kubectl output.",
		Remediation: "Move sensitive data to a Secret resource. ConfigMaps should only contain non-sensitive configuration.",
		Frameworks:  []string{FrameworkNSACISA},
	},
	"insecureCapabilities": {
		ID:          "insecureCapabilities",
		Title:       "Insecure capabilities",
		Description: "Capabilities like NET_RAW, SYS_PTRACE, and MKNOD can be exploited for packet spoofing, process injection, or device manipulation.",
		Remediation: "Remove insecure capabilities from securityContext.capabilities.add. Use capabilities.drop: [ALL] and add back only what is strictly needed.",
		Frameworks:  []string{FrameworkNSACISA, FrameworkCIS},
	},
	"hostNetwork": {
		ID:          "hostNetwork",
		Title:       "Host network",
		Description: "Sharing the host network namespace lets the container see all network traffic and access host network services.",
		Remediation: "Remove hostNetwork: true unless the workload genuinely needs host network access.",
		Frameworks:  []string{FrameworkNSACISA, FrameworkCIS},
	},
	"hostPID": {
		ID:          "hostPID",
		Title:       "Host PID namespace",
		Description: "Sharing the host PID namespace lets the container see and signal all host processes, enabling process injection attacks.",
		Remediation: "Remove hostPID: true from the pod spec.",
		Frameworks:  []string{FrameworkNSACISA, FrameworkCIS},
	},
	"hostIPC": {
		ID:          "hostIPC",
		Title:       "Host IPC namespace",
		Description: "Sharing the host IPC namespace allows access to shared memory and inter-process communication with host processes.",
		Remediation: "Remove hostIPC: true from the pod spec.",
		Frameworks:  []string{FrameworkNSACISA, FrameworkCIS},
	},
	"automountServiceAccountToken": {
		ID:          "automountServiceAccountToken",
		Title:       "Service account token auto-mount",
		Description: "Auto-mounted tokens can be used by attackers to authenticate to the Kubernetes API server and escalate privileges.",
		Remediation: "Set automountServiceAccountToken: false on the pod spec. Only enable for pods that need API access.",
		Frameworks:  []string{FrameworkNSACISA},
	},

	// ── Reliability ───────────────────────────────────────────────────
	"readinessProbeMissing": {
		ID:          "readinessProbeMissing",
		Title:       "Missing readiness probe",
		Description: "Without a readiness probe, Kubernetes sends traffic to pods before they are ready, causing errors for users.",
		Remediation: "Add a readinessProbe (HTTP GET, TCP, or exec) to each container.",
	},
	"livenessProbeMissing": {
		ID:          "livenessProbeMissing",
		Title:       "Missing liveness probe",
		Description: "Without a liveness probe, Kubernetes cannot detect and restart containers that are stuck or deadlocked.",
		Remediation: "Add a livenessProbe (HTTP GET, TCP, or exec) to each container.",
	},
	"imageTagLatest": {
		ID:          "imageTagLatest",
		Title:       "Image tag latest or missing",
		Description: "Using 'latest' or no tag means deployments are not reproducible — you cannot tell which version is running, and rollbacks may not work.",
		Remediation: "Pin images to a specific version tag (e.g., nginx:1.25.3).",
	},
	"pullPolicyNotAlways": {
		ID:          "pullPolicyNotAlways",
		Title:       "Pull policy not Always",
		Description: "With a mutable tag and non-Always pull policy, nodes may run stale cached images, causing inconsistency across replicas.",
		Remediation: "Set imagePullPolicy: Always when using mutable tags, or pin to immutable digests.",
	},
	"singleReplica": {
		ID:          "singleReplica",
		Title:       "Single replica",
		Description: "A single-replica deployment has no redundancy — any pod disruption (node drain, OOM, crash) causes downtime.",
		Remediation: "Set replicas: 2 or higher, or configure an HPA for autoscaling.",
	},
	"missingPDB": {
		ID:          "missingPDB",
		Title:       "Missing PodDisruptionBudget",
		Description: "Without a PDB, cluster operations (node drains, upgrades) can take down all replicas simultaneously.",
		Remediation: "Create a PodDisruptionBudget with minAvailable or maxUnavailable matching your availability requirements.",
	},
	"missingTopologySpread": {
		ID:          "missingTopologySpread",
		Title:       "Missing topology spread constraints",
		Description: "Without topology spread constraints, all replicas can land on the same node or availability zone, giving an illusion of HA without real fault tolerance.",
		Remediation: "Add topologySpreadConstraints to the pod template with topologyKey: kubernetes.io/hostname or topology.kubernetes.io/zone.",
	},
	"podHARisk": {
		ID:          "podHARisk",
		Title:       "All replicas on same node",
		Description: "All pod replicas are scheduled on the same node. If that node fails, all replicas go down simultaneously — no actual high availability.",
		Remediation: "Add pod anti-affinity or topology spread constraints to distribute replicas across nodes.",
	},

	// ── Efficiency ────────────────────────────────────────────────────
	"cpuRequestMissing": {
		ID:          "cpuRequestMissing",
		Title:       "Missing CPU request",
		Description: "Without CPU requests, the scheduler cannot make informed placement decisions, leading to overcommitted nodes and throttling.",
		Remediation: "Set resources.requests.cpu (e.g., 100m) on each container.",
		Frameworks:  []string{FrameworkCIS},
	},
	"memoryRequestMissing": {
		ID:          "memoryRequestMissing",
		Title:       "Missing memory request",
		Description: "Without memory requests, pods may be scheduled on nodes without enough memory, causing OOM kills.",
		Remediation: "Set resources.requests.memory (e.g., 128Mi) on each container.",
		Frameworks:  []string{FrameworkCIS},
	},
	"cpuLimitMissing": {
		ID:          "cpuLimitMissing",
		Title:       "Missing CPU limit",
		Description: "Without CPU limits, a single container can consume all CPU on a node, starving other workloads.",
		Remediation: "Set resources.limits.cpu (e.g., 500m) on each container.",
		Frameworks:  []string{FrameworkCIS},
	},
	"memoryLimitMissing": {
		ID:          "memoryLimitMissing",
		Title:       "Missing memory limit",
		Description: "Without memory limits, a container can consume unbounded memory, eventually triggering kernel OOM kills on the node.",
		Remediation: "Set resources.limits.memory (e.g., 512Mi) on each container.",
		Frameworks:  []string{FrameworkCIS},
	},
	"resourceUtilization": {
		ID:          "resourceUtilization",
		Title:       "Resource utilization mismatch",
		Description: "Pod resource usage is significantly different from its requests — either wasting resources (<10% used) or at risk of throttling/OOM (>90% used).",
		Remediation: "Adjust resources.requests to match actual usage. Use metrics or VPA recommendations to right-size.",
	},
	"orphanConfigMapSecret": {
		ID:          "orphanConfigMapSecret",
		Title:       "Unused ConfigMap or Secret",
		Description: "This ConfigMap or Secret is not referenced by any pod (env vars, volumes, or imagePullSecrets). It may be orphaned and safe to remove.",
		Remediation: "Verify this resource is no longer needed and delete it, or add a reference from a pod spec if it should be in use.",
	},

	"deprecatedAPIVersion": {
		ID:          "deprecatedAPIVersion",
		Title:       "Deprecated API version",
		Description: "This cluster still serves a deprecated API version. Resources using this API will break after a Kubernetes upgrade that removes it.",
		Remediation: "Migrate resources to the replacement API version before upgrading the cluster.",
	},

	// ── Cross-resource ────────────────────────────────────────────────
	"serviceNoMatchingPods": {
		ID:          "serviceNoMatchingPods",
		Title:       "Service has no matching pods",
		Description: "The service selector does not match any running pods — traffic sent to this service will fail.",
		Remediation: "Verify the service selector labels match the pod template labels on the target workload.",
	},
	"ingressNoMatchingService": {
		ID:          "ingressNoMatchingService",
		Title:       "Ingress references missing service",
		Description: "The ingress backend references a service that does not exist, so incoming traffic will get 503 errors.",
		Remediation: "Check the ingress spec and correct the service name, or create the missing service.",
	},

	// ── Lifecycle ─────────────────────────────────────────────────────
	"stuckTerminating": {
		ID:          "stuckTerminating",
		Title:       "Stuck terminating resource",
		Description: "This resource has metadata.deletionTimestamp set but is still alive past the cleanup window. Most controllers finish cleanup within seconds; minutes-long delays usually mean a finalizer's owning controller is unhealthy or unable to reach a dependent service. Common causes: the controller pod is CrashLoopBackOff, DNS resolution is broken, the finalizer logic depends on a webhook or external API that's unavailable.",
		Remediation: "Check the controller responsible for each finalizer key (kubectl describe will show finalizers under metadata). For Argo CD, look at argocd-application-controller in the argocd namespace. For Flux, look at the matching controller (kustomize-controller, helm-controller, source-controller) in flux-system. Once the controller is healthy, deletion will resume automatically. If you must remove a stuck resource and accept the orphaned cleanup, manually clear the finalizers field — but only as a last resort.",
	},
}
