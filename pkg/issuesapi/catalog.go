package issuesapi

// Static, human-facing catalog of issue categories for the Hub's Issues
// settings — so an org can hide categories proactively, not just the ones
// currently firing in the fleet. Descriptions live here (the category enum
// carries none); titles are intentionally NOT included — consumers label
// categories via their own map (k8s-ui categoryLabel) so the settings page and
// the live queue stay labelled identically.

// CatalogCategory is one issue category's static, displayable definition.
type CatalogCategory struct {
	Category    Category `json:"category"`
	Description string   `json:"description"`
}

// CatalogGroup is a display group with its member categories, in display order.
type CatalogGroup struct {
	Group      CategoryGroup     `json:"group"`
	Title      string            `json:"title"`
	Categories []CatalogCategory `json:"categories"`
}

// groupOrder is the display order of category groups — the lifecycle arc a
// workload moves through (schedule → start → run → wire up → …) then platform.
var groupOrder = []CategoryGroup{
	GroupScheduling, GroupStartup, GroupRuntime, GroupConfiguration,
	GroupNetworking, GroupStorage, GroupScaling, GroupSecurity, GroupControlPlane,
}

var groupTitle = map[CategoryGroup]string{
	GroupScheduling:    "Scheduling",
	GroupStartup:       "Startup",
	GroupRuntime:       "Runtime",
	GroupConfiguration: "Configuration",
	GroupNetworking:    "Networking",
	GroupStorage:       "Storage",
	GroupScaling:       "Scaling",
	GroupSecurity:      "Security",
	GroupControlPlane:  "Control plane",
}

// GroupTitle returns the display label for a category group.
func GroupTitle(g CategoryGroup) string {
	if t, ok := groupTitle[g]; ok {
		return t
	}
	return string(g)
}

// catalogOrder is every real category (excluding "unknown") in display order
// within its group. TestCatalogComplete keeps it in lockstep with the category
// enum + group map.
var catalogOrder = []Category{
	// Scheduling
	CategoryUnschedulable, CategoryQuotaExceeded, CategoryAdmissionWebhookBlocking,
	// Startup
	CategoryImagePullFailed, CategoryContainerWaiting, CategoryInitContainerFailed,
	// Runtime
	CategoryCrashLoop, CategoryOOMKilled, CategoryLivenessProbeFail, CategoryReadinessFailed,
	CategoryWorkloadDegraded, CategoryHighRestart, CategoryJobFailed, CategoryCronJobFailed,
	// Configuration
	CategoryMissingConfigRef, CategoryPDBBlocksEvictions, CategorySecretSyncFailed,
	// Networking
	CategoryServiceNoEndpoints, CategoryIngressBackendMissing, CategoryLoadBalancerPending,
	CategoryGatewayNotReady, CategoryGatewayRouteInvalid, CategoryDNSFailure,
	// Storage
	CategoryPVCPending, CategoryPVCLost, CategoryPVFailed, CategoryPVCResizeFailed,
	CategoryVolumeMountFailed, CategoryVolumeAccessModeConflict,
	// Scaling
	CategoryRolloutStalled, CategoryHPALimitedOrFailed,
	// Security
	CategoryRBACForbidden, CategoryCertificateNotReady, CategoryPodSecurityViolation,
	// Control plane
	CategoryTerminationStuck, CategoryNodeNotReady, CategoryAPIServiceUnavailable,
	CategoryNodeProvisioningFail, CategoryCrossplaneReconcile, CategoryOperatorConditionFail,
	CategoryGitOpsSyncFailed, CategoryGitOpsRenderFailed, CategoryGitOpsSpecInvalid,
	CategoryGitOpsOperationFailed, CategoryGitOpsOutOfSync, CategoryGitOpsHealthDegraded,
	CategoryHelmReleaseFailed, CategoryWebhookBackendDown, CategoryControlPlaneNotReady, CategoryMachineNotReady,
}

// categoriesWithoutDetector are enum categories Radar doesn't emit yet — the
// detection layer (internal/issues/category.go) has no path that produces them.
// They're deliberately excluded from the catalog: a settings registry must not
// imply Radar detects something it can't (you can't hide what never fires).
// When a detector lands, move the category into catalogOrder + categoryDescription
// and drop it from here. TestCatalogComplete enforces both directions.
var categoriesWithoutDetector = map[Category]bool{
	CategoryNetworkPolicyBlock: true,
}

var categoryDescription = map[Category]string{
	// Scheduling
	CategoryUnschedulable:            "Pods can't be placed on any node — none satisfies their CPU/memory requests, node selector, affinity, taints, or topology constraints.",
	CategoryQuotaExceeded:            "A ResourceQuota or LimitRange rejected the workload — the namespace is out of its CPU, memory, or object budget.",
	CategoryAdmissionWebhookBlocking: "An admission webhook is rejecting the resource — a validating or mutating webhook denied or errored on the request.",
	// Startup
	CategoryImagePullFailed:     "A container image can't be pulled — wrong name/tag, a private registry without credentials, or the registry is unreachable (ImagePullBackOff).",
	CategoryContainerWaiting:    "A container is stuck Waiting and never reached Running — blocked on config, secrets, volumes, its image, or a pod sandbox / IP from the CNI.",
	CategoryInitContainerFailed: "An init container is failing or looping, so the main containers never start.",
	// Runtime
	CategoryCrashLoop:         "A container keeps crashing and restarting (CrashLoopBackOff) — it exits non-zero shortly after starting.",
	CategoryOOMKilled:         "A container was OOMKilled — it hit its own memory limit, or the node ran out of memory. Check usage vs limits and node memory pressure before raising limits.",
	CategoryLivenessProbeFail: "The liveness probe keeps failing, so the kubelet repeatedly restarts the container.",
	CategoryReadinessFailed:   "The readiness probe is failing, so the pod is kept out of Service endpoints and receives no traffic.",
	CategoryWorkloadDegraded:  "A workload has fewer ready replicas than desired — some pods are unavailable.",
	CategoryHighRestart:       "A container has restarted many times — unstable even if it's currently running.",
	CategoryJobFailed:         "A Job failed — it exhausted its retries (backoffLimit) or hit its deadline — or has been running too long with no completions.",
	CategoryCronJobFailed:     "A CronJob's recent runs are failing, or its schedule isn't producing successful jobs.",
	// Configuration
	CategoryMissingConfigRef:   "A pod references a ConfigMap, Secret, or volume that doesn't exist, so it can't start.",
	CategoryPDBBlocksEvictions: "A PodDisruptionBudget is blocking evictions — node drains and upgrades can't make progress.",
	CategorySecretSyncFailed:   "An external secret sync (e.g. External Secrets Operator) failed to materialize a Secret.",
	// Networking
	CategoryServiceNoEndpoints:    "A Service has no ready endpoints — its selector matches no ready pods, so traffic to it fails.",
	CategoryIngressBackendMissing: "An Ingress points at a Service that doesn't exist — incoming requests get 503s.",
	CategoryLoadBalancerPending:   "A LoadBalancer Service is stuck Pending — the cloud controller hasn't provisioned an external IP.",
	CategoryGatewayNotReady:       "A Gateway (Gateway API) isn't accepted or programmed — its listeners aren't serving.",
	CategoryGatewayRouteInvalid:   "An HTTPRoute (or other route) was rejected or not accepted by its parent Gateway.",
	CategoryDNSFailure:            "CoreDNS's Corefile has a rule (an NXDOMAIN template or a rewrite) that can override Kubernetes service DNS — a misconfiguration that risks breaking in-cluster name resolution.",
	// Storage
	CategoryPVCPending:               "A PersistentVolumeClaim is stuck Pending — no matching volume and provisioning hasn't completed.",
	CategoryPVCLost:                  "A PersistentVolumeClaim's bound volume was lost — the underlying PersistentVolume is gone.",
	CategoryPVFailed:                 "A PersistentVolume is in Failed state — its reclaim or backing storage failed.",
	CategoryPVCResizeFailed:          "A volume expansion didn't complete — the requested resize failed or is stuck.",
	CategoryVolumeMountFailed:        "A pod can't mount a volume — attach/mount failed (wrong node, missing CSI driver, or permissions).",
	CategoryVolumeAccessModeConflict: "A volume's access mode conflicts with how it's mounted (e.g. an RWO volume claimed by pods on different nodes).",
	// Scaling
	CategoryRolloutStalled:     "A workload rollout is stuck — the new revision isn't progressing (progressDeadlineExceeded).",
	CategoryHPALimitedOrFailed: "A HorizontalPodAutoscaler can't scale — missing metrics, pinned at max replicas, or scaling errors.",
	// Security
	CategoryRBACForbidden:        "A workload can't create its pods — its controller's ServiceAccount is denied pod creation by RBAC.",
	CategoryCertificateNotReady:  "A cert-manager Certificate isn't issued — issuance is failing or still pending.",
	CategoryPodSecurityViolation: "Pod Security admission is rejecting pods — they violate the namespace's enforced Pod Security Standard.",
	// Control plane
	CategoryTerminationStuck:      "A resource is stuck Terminating past the cleanup window — a finalizer's owning controller is unhealthy.",
	CategoryNodeNotReady:          "A node is NotReady — its kubelet isn't reporting healthy, putting the pods on it at risk.",
	CategoryAPIServiceUnavailable: "An aggregated APIService is unavailable — its backing extension API server isn't responding.",
	CategoryNodeProvisioningFail:  "Node provisioning failed — the autoscaler or Karpenter couldn't bring up a node.",
	CategoryCrossplaneReconcile:   "A Crossplane managed or composite resource can't reconcile — its Ready or Synced condition is False.",
	CategoryOperatorConditionFail: "An operator-managed resource is reporting a failed status condition.",
	CategoryGitOpsSyncFailed:      "A GitOps app failed to sync (catch-all) — Argo CD or Flux couldn't reconcile it to the desired state.",
	CategoryGitOpsRenderFailed:    "GitOps couldn't render manifests from Git — a kustomize/helm build or source fetch failed.",
	CategoryGitOpsSpecInvalid:     "A GitOps app's spec is invalid — a bad destination, source, or project reference.",
	CategoryGitOpsOperationFailed: "A GitOps sync ran but failed — a resource apply, install, or upgrade errored.",
	CategoryGitOpsOutOfSync:       "Live state has drifted from Git — the GitOps app is OutOfSync.",
	CategoryGitOpsHealthDegraded:  "A GitOps app's managed resources are unhealthy or missing.",
	CategoryHelmReleaseFailed:     "A native Helm release is failed or stuck pending — its latest install, upgrade, or rollback did not complete cleanly.",
	CategoryWebhookBackendDown:    "An admission webhook points at a backend Service that doesn't exist — matching requests are blocked (failurePolicy=Fail) or silently bypassed (Ignore).",
	CategoryControlPlaneNotReady:  "A managed control plane (Cluster API) isn't ready — the cluster's control plane is unhealthy or still provisioning.",
	CategoryMachineNotReady:       "A Cluster API Machine isn't ready — the underlying instance failed to join or is unhealthy.",
}

// Catalog returns every issue category grouped and ordered for display, each
// with its one-line description. Static (compiled in) — no cluster data needed.
func Catalog() []CatalogGroup {
	out := make([]CatalogGroup, 0, len(groupOrder))
	for _, g := range groupOrder {
		var cats []CatalogCategory
		for _, c := range catalogOrder {
			if GroupOf(c) == g {
				cats = append(cats, CatalogCategory{Category: c, Description: categoryDescription[c]})
			}
		}
		if len(cats) > 0 {
			out = append(out, CatalogGroup{Group: g, Title: GroupTitle(g), Categories: cats})
		}
	}
	return out
}
