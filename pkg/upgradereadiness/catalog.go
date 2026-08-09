package upgradereadiness

const ReviewedThrough = "1.36"

var (
	versionSkewReferences = []Reference{{
		Title: "Kubernetes version skew policy",
		URL:   "https://kubernetes.io/releases/version-skew-policy/",
	}}
	manifestAPIReferences = []Reference{{
		Title: "Kubernetes deprecated API migration guide",
		URL:   "https://kubernetes.io/docs/reference/using-api/deprecation-guide/",
	}}
	cloudMetricsPermissionReference = Reference{
		Title: "Radar Cloud metrics permission",
		URL:   "https://github.com/skyhook-io/radar/blob/main/docs/authentication.md#cloud-mode-helm-bindings",
	}
	gitRepoReferences = []Reference{
		{Title: "Kubernetes volumes: gitRepo", URL: "https://kubernetes.io/docs/concepts/storage/volumes/#gitrepo"},
		{Title: "Kubernetes 1.36 removal announcement", URL: "https://kubernetes.io/blog/2026/04/22/kubernetes-v1-36-release/#removal-of-the-gitrepo-volume-driver"},
	}
	externalIPReferences = []Reference{{
		Title: "Service externalIPs deprecation",
		URL:   "https://kubernetes.io/blog/2026/05/14/kubernetes-v1-36-deprecation-and-removal-of-service-externalips/",
	}}
	changelog136References = []Reference{{
		Title: "Kubernetes 1.36 urgent upgrade notes",
		URL:   "https://github.com/kubernetes/kubernetes/blob/master/CHANGELOG/CHANGELOG-1.36.md#urgent-upgrade-notes",
	}}
	cgroupReferences = []Reference{
		{Title: "Kubernetes kubelet metrics reference", URL: "https://kubernetes.io/docs/reference/instrumentation/metrics/"},
		{Title: "Kubernetes 1.35 cgroup v1 deprecation", URL: "https://github.com/kubernetes/kubernetes/blob/master/CHANGELOG/CHANGELOG-1.35.md#deprecation"},
		{Title: "KEP-4569: cgroup v1 maintenance mode", URL: "https://github.com/kubernetes/enhancements/tree/master/keps/sig-node/4569-cgroup-v1-maintenance-mode"},
		{Title: "KEP-5573: Remove cgroup v1 support", URL: "https://github.com/kubernetes/enhancements/tree/master/keps/sig-node/5573-remove-cgroup-v1"},
	}
	runtimeReferences              = []Reference{{Title: "Kubernetes kubelet metrics reference", URL: "https://kubernetes.io/docs/reference/instrumentation/metrics/"}, {Title: "Kubernetes CRI cgroup driver support policy", URL: "https://kubernetes.io/blog/2025/09/12/kubernetes-v1-34-cri-cgroup-driver-lookup-now-ga/"}}
	drainReferences                = []Reference{{Title: "Safely drain a node", URL: "https://kubernetes.io/docs/tasks/administer-cluster/safely-drain-node/"}, {Title: "Configure PodDisruptionBudgets", URL: "https://kubernetes.io/docs/tasks/run-application/configure-pdb/"}, {Title: "PodDisruptionBudget API", URL: "https://kubernetes.io/docs/reference/kubernetes-api/policy-resources/pod-disruption-budget-v1/"}}
	admissionReferences            = []Reference{{Title: "Admission webhook good practices", URL: "https://kubernetes.io/docs/concepts/cluster-administration/admission-webhooks-good-practices/"}, {Title: "Dynamic admission control", URL: "https://kubernetes.io/docs/reference/access-authn-authz/extensible-admission-controllers/"}}
	admissionMatchPolicyReferences = []Reference{{Title: "Match all API versions", URL: "https://kubernetes.io/docs/concepts/cluster-administration/admission-webhooks-good-practices/#match-all-versions"}}
	admissionBackendReferences     = []Reference{
		{Title: "Admission webhook failure policy", URL: "https://kubernetes.io/docs/reference/access-authn-authz/extensible-admission-controllers/#failure-policy"},
		{Title: "Webhook availability guidance", URL: "https://kubernetes.io/docs/concepts/cluster-administration/admission-webhooks-good-practices/#ha-deployment"},
	}
	admissionAuthReviewReferences = []Reference{{Title: "Limit admission webhook scope", URL: "https://kubernetes.io/docs/concepts/cluster-administration/admission-webhooks-good-practices/#webhook-limit-scope"}}
	crdConversionReferences       = []Reference{{Title: "CRD webhook conversion", URL: "https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definition-versioning/#webhook-conversion"}}
	apiServiceReferences          = []Reference{{Title: "Kubernetes API aggregation layer", URL: "https://kubernetes.io/docs/concepts/extend-kubernetes/api-extension/apiserver-aggregation/"}, {Title: "APIService API", URL: "https://kubernetes.io/docs/reference/kubernetes-api/apiregistration/api-service-v1/"}}
	strictIPReferences            = []Reference{{Title: "KEP-4858: stricter validation of IP and CIDR fields", URL: "https://github.com/kubernetes/enhancements/tree/master/keps/sig-network/4858-ip-cidr-validation"}}
	gkeExecProbeReferences        = []Reference{{Title: "GKE exec probe timeout behavior", URL: "https://cloud.google.com/kubernetes-engine/docs/deprecations/exec-probe-timeouts"}}
)
