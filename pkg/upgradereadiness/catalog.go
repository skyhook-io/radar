package upgradereadiness

const ReviewedThrough = "1.37"

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
	admissionAuthReviewReferences     = []Reference{{Title: "Limit admission webhook scope", URL: "https://kubernetes.io/docs/concepts/cluster-administration/admission-webhooks-good-practices/#webhook-limit-scope"}}
	crdConversionReferences           = []Reference{{Title: "CRD webhook conversion", URL: "https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definition-versioning/#webhook-conversion"}}
	apiServiceReferences              = []Reference{{Title: "Kubernetes API aggregation layer", URL: "https://kubernetes.io/docs/concepts/extend-kubernetes/api-extension/apiserver-aggregation/"}, {Title: "APIService API", URL: "https://kubernetes.io/docs/reference/kubernetes-api/apiregistration/api-service-v1/"}}
	strictIPReferences                = []Reference{{Title: "KEP-4858: stricter validation of IP and CIDR fields", URL: "https://github.com/kubernetes/enhancements/tree/master/keps/sig-network/4858-ip-cidr-validation"}}
	gkeExecProbeReferences            = []Reference{{Title: "GKE exec probe timeout behavior", URL: "https://cloud.google.com/kubernetes-engine/docs/deprecations/exec-probe-timeouts"}}
	changelog137References            = []Reference{{Title: "Kubernetes 1.37 release notes", URL: "https://github.com/kubernetes/kubernetes/blob/release-1.37/CHANGELOG/CHANGELOG-1.37.md"}}
	featureGateLifecycleReferences137 = []Reference{
		{Title: "Kubernetes 1.36 versioned feature lifecycle", URL: "https://github.com/kubernetes/kubernetes/blob/release-1.36/test/compatibility_lifecycle/reference/versioned_feature_list.yaml"},
		{Title: "Kubernetes 1.37 versioned feature lifecycle", URL: "https://github.com/kubernetes/kubernetes/blob/release-1.37/test/compatibility_lifecycle/reference/versioned_feature_list.yaml"},
	}
	removedFeatureGateReferences137 = map[string][]Reference{
		"AnonymousAuthConfigurableEndpoints":    append([]Reference(nil), featureGateLifecycleReferences137...),
		"APIServerTracing":                      {{Title: "Remove locked GA feature gates", URL: "https://github.com/kubernetes/kubernetes/pull/138907"}},
		"AnyVolumeDataSource":                   {{Title: "Remove the AnyVolumeDataSource feature gate", URL: "https://github.com/kubernetes/kubernetes/pull/135336"}},
		"AuthorizeNodeWithSelectors":            append([]Reference(nil), featureGateLifecycleReferences137...),
		"AuthorizeWithSelectors":                append([]Reference(nil), featureGateLifecycleReferences137...),
		"BtreeWatchCache":                       {{Title: "Remove locked GA feature gates", URL: "https://github.com/kubernetes/kubernetes/pull/138907"}},
		"ConsistentListFromCache":               {{Title: "Remove locked GA feature gates", URL: "https://github.com/kubernetes/kubernetes/pull/138907"}},
		"GangScheduling":                        {{Title: "Merge scheduling feature gates into GenericWorkload", URL: "https://github.com/kubernetes/kubernetes/pull/139520"}},
		"JobBackoffLimitPerIndex":               append([]Reference(nil), featureGateLifecycleReferences137...),
		"JobPodReplacementPolicy":               append([]Reference(nil), featureGateLifecycleReferences137...),
		"JobSuccessPolicy":                      append([]Reference(nil), featureGateLifecycleReferences137...),
		"LogarithmicScaleDown":                  append([]Reference(nil), featureGateLifecycleReferences137...),
		"OrderedNamespaceDeletion":              {{Title: "Remove locked GA feature gates", URL: "https://github.com/kubernetes/kubernetes/pull/138907"}},
		"PodLifecycleSleepAction":               append([]Reference(nil), featureGateLifecycleReferences137...),
		"PodLifecycleSleepActionAllowZero":      append([]Reference(nil), featureGateLifecycleReferences137...),
		"PreventStaticPodAPIReferences":         {{Title: "Remove PreventStaticPodAPIReferences", URL: "https://github.com/kubernetes/kubernetes/pull/140226"}},
		"RelaxedDNSSearchValidation":            {{Title: "Remove RelaxedDNSSearchValidation", URL: "https://github.com/kubernetes/kubernetes/pull/139217"}},
		"ResilientWatchCacheInitialization":     {{Title: "Remove locked GA feature gates", URL: "https://github.com/kubernetes/kubernetes/pull/138907"}},
		"RetryGenerateName":                     {{Title: "Remove locked GA feature gates", URL: "https://github.com/kubernetes/kubernetes/pull/138907"}},
		"SchedulerQueueingHints":                append([]Reference(nil), featureGateLifecycleReferences137...),
		"SidecarContainers":                     {{Title: "Remove SidecarContainers", URL: "https://github.com/kubernetes/kubernetes/pull/137755"}},
		"StreamingCollectionEncodingToJSON":     {{Title: "Remove locked GA feature gates", URL: "https://github.com/kubernetes/kubernetes/pull/138907"}},
		"StreamingCollectionEncodingToProtobuf": {{Title: "Remove locked GA feature gates", URL: "https://github.com/kubernetes/kubernetes/pull/138907"}},
		"StructuredAuthenticationConfiguration": append([]Reference(nil), featureGateLifecycleReferences137...),
		"WorkloadAwarePreemption":               {{Title: "Merge scheduling feature gates into GenericWorkload", URL: "https://github.com/kubernetes/kubernetes/pull/139520"}},
	}
	lockedFeatureGateReferences137 = map[string][]Reference{
		"DeclarativeValidationTakeover":           {{Title: "Lock DeclarativeValidationTakeover to its default", URL: "https://github.com/kubernetes/kubernetes/pull/139212"}},
		"DisableCPUQuotaWithExclusiveCPUs":        append([]Reference(nil), featureGateLifecycleReferences137...),
		"DRAExtendedResource":                     append([]Reference(nil), featureGateLifecycleReferences137...),
		"DRAPrioritizedList":                      {{Title: "Lock DRAPrioritizedList to its default", URL: "https://github.com/kubernetes/kubernetes/pull/139110"}},
		"DRAResourceClaimDeviceStatus":            append([]Reference(nil), featureGateLifecycleReferences137...),
		"HostnameOverride":                        {{Title: "Lock HostnameOverride to its default", URL: "https://github.com/kubernetes/kubernetes/pull/139116"}},
		"HPAConfigurableTolerance":                append([]Reference(nil), featureGateLifecycleReferences137...),
		"InPlacePodVerticalScalingInitContainers": append([]Reference(nil), featureGateLifecycleReferences137...),
		"NodeDeclaredFeatures":                    append([]Reference(nil), featureGateLifecycleReferences137...),
		"PLEGOnDemandRelist":                      append([]Reference(nil), featureGateLifecycleReferences137...),
		"PodReadyToStartContainersCondition":      append([]Reference(nil), featureGateLifecycleReferences137...),
		"RelaxedServiceNameValidation":            append([]Reference(nil), featureGateLifecycleReferences137...),
		"WatchCacheInitializationPostStartHook":   append([]Reference(nil), featureGateLifecycleReferences137...),
	}
	componentFlagReferences137     = []Reference{{Title: "Remove no-op cloud provider controller registrations", URL: "https://github.com/kubernetes/kubernetes/pull/138002"}}
	podGroupAdmissionReferences137 = []Reference{{Title: "Remove the PodGroupWorkloadExists admission plugin", URL: "https://github.com/kubernetes/kubernetes/pull/139008"}, {Title: "Kubernetes 1.37 changelog", URL: "https://github.com/kubernetes/kubernetes/blob/release-1.37/CHANGELOG/CHANGELOG-1.37.md"}}
	cAdvisorRemovalReferences137   = []Reference{{Title: "Remove deprecated cAdvisor flags and metrics", URL: "https://github.com/kubernetes/kubernetes/pull/139870"}, {Title: "Kubernetes 1.37 changelog", URL: "https://github.com/kubernetes/kubernetes/blob/release-1.37/CHANGELOG/CHANGELOG-1.37.md"}}
	eventRecordQPSReferences137    = []Reference{
		{Title: "Make eventRecordQPS zero mean unlimited", URL: "https://github.com/kubernetes/kubernetes/pull/117119"},
		{Title: "Kubelet 1.36 passes eventRecordQPS to client-go", URL: "https://github.com/kubernetes/kubernetes/blob/v1.36.0/cmd/kubelet/app/server.go#L726-L730"},
		{Title: "client-go 1.36 default QPS is 5", URL: "https://github.com/kubernetes/kubernetes/blob/v1.36.0/staging/src/k8s.io/client-go/rest/config.go#L46-L49"},
		{Title: "client-go 1.36 applies its default when QPS is zero", URL: "https://github.com/kubernetes/kubernetes/blob/v1.36.0/staging/src/k8s.io/client-go/rest/config.go#L370-L375"},
	}
	schedulingAPIReferences137      = []Reference{{Title: "Promote Workload and PodGroup APIs to v1beta1", URL: "https://github.com/kubernetes/kubernetes/pull/140184"}}
	kubeadmV1Beta3References137     = []Reference{{Title: "Remove kubeadm v1beta3 and PublicKeysECDSA", URL: "https://github.com/kubernetes/kubernetes/pull/136016"}}
	kubeadmFeatureGateReferences137 = map[string][]Reference{
		"NodeLocalCRISocket": {{Title: "Remove the NodeLocalCRISocket feature gate", URL: "https://github.com/kubernetes/kubernetes/pull/138645"}},
		"PublicKeysECDSA":    append([]Reference(nil), kubeadmV1Beta3References137...),
	}
	metricReferences137 = map[string][]Reference{
		"resourceclaim_controller_creates_total":   {{Title: "Harmonize DRA ResourceClaim creation metrics", URL: "https://github.com/kubernetes/kubernetes/pull/138542"}},
		"scheduler_resourceclaim_creates_total":    {{Title: "Harmonize DRA ResourceClaim creation metrics", URL: "https://github.com/kubernetes/kubernetes/pull/138542"}},
		"resourceclaim_controller_resource_claims": {{Title: "Harmonize DRA ResourceClaim creation metrics", URL: "https://github.com/kubernetes/kubernetes/pull/138542"}},
		"container_cpu_load_average_10s":           append([]Reference(nil), cAdvisorRemovalReferences137...),
		"container_cpu_load_d_average_10s":         append([]Reference(nil), cAdvisorRemovalReferences137...),
		"container_tasks_state":                    append([]Reference(nil), cAdvisorRemovalReferences137...),
		"container_application_*":                  append([]Reference(nil), cAdvisorRemovalReferences137...),
	}
	ipvsDeprecationReferences = []Reference{
		{Title: "KEP-5495: Deprecate IPVS mode in kube-proxy", URL: "https://github.com/kubernetes/enhancements/tree/master/keps/sig-network/5495-deprecate-ipvs-mode-in-kube-proxy"},
		{Title: "Add the KubeProxyIPVS feature gate", URL: "https://github.com/kubernetes/kubernetes/pull/139397"},
	}
	nftablesDefaultReferences = []Reference{
		{Title: "KEP-5343: Make nftables the default kube-proxy backend", URL: "https://github.com/kubernetes/enhancements/tree/master/keps/sig-network/5343-nftables-to-default"},
		{Title: "Warn when kube-proxy mode uses the implicit default", URL: "https://github.com/kubernetes/kubernetes/pull/139957"},
	}
	kubeProxyModeReferences = append(append([]Reference(nil), ipvsDeprecationReferences...), nftablesDefaultReferences...)
	selinuxMountReferences  = []Reference{
		{Title: "SELinux volume label changes", URL: "https://kubernetes.io/blog/2026/04/22/breaking-changes-in-selinux-volume-labeling/"},
		{Title: "Graduate SELinuxMount to GA", URL: "https://github.com/kubernetes/kubernetes/pull/139956"},
	}
)
