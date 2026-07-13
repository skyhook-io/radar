package topology

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/health"
	"github.com/skyhook-io/radar/pkg/perfstats"
)

// Builder constructs topology graphs from K8s resources
type Builder struct {
	provider ResourceProvider
	dynamic  DynamicProvider
}

// NewBuilder creates a new topology builder
func NewBuilder(provider ResourceProvider) *Builder {
	return &Builder{
		provider: provider,
	}
}

// WithDynamic sets the dynamic provider for CRD support
func (b *Builder) WithDynamic(dp DynamicProvider) *Builder {
	b.dynamic = dp
	return b
}

// Build constructs a topology based on the given options
func (b *Builder) Build(opts BuildOptions) (*Topology, error) {
	if b.provider == nil {
		return nil, fmt.Errorf("resource provider not initialized")
	}

	start := time.Now()

	// Detect large cluster and apply optimizations
	isLargeCluster, hiddenKinds, estimatedNodes := b.detectLargeClusterAndOptimize(&opts)

	// Large clusters without a namespace filter: skip the expensive build entirely.
	// The frontend shows a "select namespace" prompt instead of a blank graph.
	// ForRelationshipCache bypasses this guard — internal builds need the full graph
	// for resource detail "Related Resources" lookups.
	if isLargeCluster && len(opts.Namespaces) == 0 && !opts.ForRelationshipCache {
		perfstats.RecordTopologyBuild(time.Since(start), 0, 0, estimatedNodes)
		return &Topology{
			Nodes:                   []Node{},
			Edges:                   []Edge{},
			Warnings:                []string{"Cluster too large for all-namespace topology. Filter to a specific namespace."},
			LargeCluster:            true,
			HiddenKinds:             hiddenKinds,
			RequiresNamespaceFilter: true,
			EstimatedNodes:          estimatedNodes,
		}, nil
	}

	// Summary mode: a namespace the user has filtered to is still big enough
	// to hang the tab if we render every pod. Collapse the pod tier into
	// per-workload / per-service counts. Only kicks in for real (non-cache)
	// namespace-filtered builds — the all-namespace large path already returns
	// the RequiresNamespaceFilter prompt above, and the relationship cache
	// needs the full graph.
	if estimatedNodes >= SummaryModeThreshold && len(opts.Namespaces) > 0 && !opts.ForRelationshipCache {
		opts.SummaryMode = true
	}

	var topo *Topology
	var err error

	switch opts.ViewMode {
	case ViewModeTraffic:
		topo, err = b.buildTrafficTopology(opts)
	default:
		topo, err = b.buildResourcesTopology(opts)
	}

	if err != nil {
		return nil, err
	}

	// Set large cluster flags in response
	if isLargeCluster {
		topo.LargeCluster = true
		topo.HiddenKinds = hiddenKinds
	}
	topo.EstimatedNodes = estimatedNodes
	topo.SummaryMode = opts.SummaryMode

	perfstats.RecordTopologyBuild(time.Since(start), len(topo.Nodes), len(topo.Edges), estimatedNodes)
	return topo, nil
}

// detectLargeClusterAndOptimize checks if cluster is large and applies optimizations.
// Returns: large-cluster flag, hidden kinds, and the estimated node count itself
// (exposed so callers — eg. the SSE broadcaster — can drive debounce / render-mode
// decisions off the same signal that drives the in-builder optimizations here).
func (b *Builder) detectLargeClusterAndOptimize(opts *BuildOptions) (bool, []string, int) {
	// Quick count of workload resources to estimate total node count
	// This is a lightweight check - we count core resources that contribute most to topology
	estimatedNodes := 0
	var hiddenKinds []string

	// Count deployments
	if deployments, _ := b.provider.Deployments(); deployments != nil {
		for _, d := range deployments {
			if opts.MatchesNamespaceFilter(d.Namespace) {
				estimatedNodes++
			}
		}
	}

	// Count statefulsets
	if statefulsets, _ := b.provider.StatefulSets(); statefulsets != nil {
		for _, s := range statefulsets {
			if opts.MatchesNamespaceFilter(s.Namespace) {
				estimatedNodes++
			}
		}
	}

	// Count daemonsets
	if daemonsets, _ := b.provider.DaemonSets(); daemonsets != nil {
		for _, d := range daemonsets {
			if opts.MatchesNamespaceFilter(d.Namespace) {
				estimatedNodes++
			}
		}
	}

	// Count services
	if services, _ := b.provider.Services(); services != nil {
		for _, s := range services {
			if opts.MatchesNamespaceFilter(s.Namespace) {
				estimatedNodes++
			}
		}
	}

	// Count pods (this is usually the largest contributor)
	if pods, _ := b.provider.Pods(); pods != nil {
		podCount := 0
		for _, p := range pods {
			if opts.MatchesNamespaceFilter(p.Namespace) {
				podCount++
			}
		}
		// Estimate pod nodes after grouping (assume ~5 pods per group on average)
		estimatedNodes += (podCount + 4) / 5
	}

	// Count jobs and cronjobs
	if jobs, _ := b.provider.Jobs(); jobs != nil {
		for _, j := range jobs {
			if opts.MatchesNamespaceFilter(j.Namespace) {
				estimatedNodes++
			}
		}
	}
	if cronjobs, _ := b.provider.CronJobs(); cronjobs != nil {
		for _, c := range cronjobs {
			if opts.MatchesNamespaceFilter(c.Namespace) {
				estimatedNodes++
			}
		}
	}

	// Count ingresses
	if ingresses, _ := b.provider.Ingresses(); ingresses != nil {
		for _, i := range ingresses {
			if opts.MatchesNamespaceFilter(i.Namespace) {
				estimatedNodes++
			}
		}
	}

	// Count configmaps (only if currently included)
	if opts.IncludeConfigMaps {
		if configmaps, _ := b.provider.ConfigMaps(); configmaps != nil {
			for _, c := range configmaps {
				if opts.MatchesNamespaceFilter(c.Namespace) {
					estimatedNodes++
				}
			}
		}
	}

	// Count PVCs (only if currently included)
	if opts.IncludePVCs {
		if pvcs, _ := b.provider.PersistentVolumeClaims(); pvcs != nil {
			for _, p := range pvcs {
				if opts.MatchesNamespaceFilter(p.Namespace) {
					estimatedNodes++
				}
			}
		}
	}

	// Check if large cluster
	if estimatedNodes < LargeClusterThreshold {
		return false, nil, estimatedNodes
	}

	// Large cluster detected - apply optimizations
	log.Printf("INFO [topology] Large cluster detected (%d estimated nodes >= %d threshold), applying optimizations", estimatedNodes, LargeClusterThreshold)

	// 1. More aggressive pod grouping (threshold 2 instead of 5)
	opts.MaxIndividualPods = 2

	// 2. Auto-hide ConfigMaps and PVCs
	if opts.IncludeConfigMaps {
		opts.IncludeConfigMaps = false
		hiddenKinds = append(hiddenKinds, "ConfigMap")
	}
	if opts.IncludePVCs {
		opts.IncludePVCs = false
		hiddenKinds = append(hiddenKinds, "PersistentVolumeClaim")
	}

	return true, hiddenKinds, estimatedNodes
}

// workloadRefKey identifies a referenced ConfigMap/Secret/PVC by namespace and
// name — the key for inverting the workload→names ref maps into a consumer index.
type workloadRefKey struct {
	namespace string
	name      string
}

// buildConsumerIndex inverts a workload→referenced-names map into a
// (namespace,name)→workloadIDs index. Building it once lets each
// ConfigMap/Secret/PVC node look up its referencing workloads directly instead
// of scanning every workload per node (O(workloads+refs) total vs O(nodes×workloads)).
func buildConsumerIndex(refs map[string]map[string]bool, workloadNamespaces map[string]string) map[workloadRefKey][]string {
	idx := make(map[workloadRefKey][]string)
	for workloadID, names := range refs {
		ns := workloadNamespaces[workloadID]
		for name := range names {
			key := workloadRefKey{namespace: ns, name: name}
			idx[key] = append(idx[key], workloadID)
		}
	}
	return idx
}

// buildResourcesTopology creates a comprehensive resource view
func (b *Builder) buildResourcesTopology(opts BuildOptions) (*Topology, error) {
	nodes := make([]Node, 0)
	edges := make([]Edge, 0)
	warnings := make([]string, 0)

	// Track IDs for linking
	deploymentIDs := make(map[string]string)
	rolloutIDs := make(map[string]string) // Argo Rollouts
	statefulSetIDs := make(map[string]string)
	replicaSetIDs := make(map[string]string)
	replicaSetToDeployment := make(map[string]string) // rsKey -> deploymentID (for shortcut edges)
	replicaSetToRollout := make(map[string]string)    // rsKey -> rolloutID (for shortcut edges)
	serviceIDs := make(map[string]string)
	jobIDs := make(map[string]string)
	cronJobIDs := make(map[string]string)
	jobToCronJob := make(map[string]string) // jobKey -> cronJobID (for shortcut edges)
	scaledJobIDs := make(map[string]string)
	jobToScaledJob := make(map[string]string) // jobKey -> scaledJobID (for shortcut edges)
	workflowIDs := make(map[string]string)
	cronWorkflowIDs := make(map[string]string)
	workflowToCronWorkflow := make(map[string]string) // workflowKey -> cronWorkflowID (for shortcut edges)
	workflowTemplateIDs := make(map[string]string)
	clusterWorkflowTemplateIDs := make(map[string]string)
	workflowTemplateNodes := make(map[string]Node)

	// Track ConfigMap/Secret/PVC references from workloads
	// Maps workloadID -> set of resource names
	workloadConfigMapRefs := make(map[string]map[string]bool)
	workloadSecretRefs := make(map[string]map[string]bool)
	workloadPVCRefs := make(map[string]map[string]bool)
	workloadServiceAccountRefs := make(map[string]string)
	// Track workload namespaces for cross-namespace validation
	workloadNamespaces := make(map[string]string) // workloadID -> namespace
	trackWorkloadRefs := func(workloadID, namespace string, refs workloadRefs) {
		if len(refs.configMaps) > 0 || len(refs.secrets) > 0 || len(refs.pvcs) > 0 || refs.serviceAccount != "" {
			workloadNamespaces[workloadID] = namespace
		}
		if len(refs.configMaps) > 0 {
			workloadConfigMapRefs[workloadID] = refs.configMaps
		}
		if len(refs.secrets) > 0 {
			workloadSecretRefs[workloadID] = refs.secrets
		}
		if len(refs.pvcs) > 0 {
			workloadPVCRefs[workloadID] = refs.pvcs
		}
		if refs.serviceAccount != "" {
			workloadServiceAccountRefs[workloadID] = refs.serviceAccount
		}
	}

	// 1. Add Deployment nodes
	var deployments []*appsv1.Deployment
	{
		deps, depsErr := b.provider.Deployments()
		if depsErr != nil {
			log.Printf("WARNING [topology] Failed to list Deployments: %v", depsErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list Deployments: %v", depsErr))
		}
		deployments = deps
	}
	for _, deploy := range deployments {
		if !opts.MatchesNamespaceFilter(deploy.Namespace) {
			continue
		}

		deployID := fmt.Sprintf("deployment/%s/%s", deploy.Namespace, deploy.Name)
		deploymentIDs[deploy.Namespace+"/"+deploy.Name] = deployID

		ready := deploy.Status.ReadyReplicas
		total := int32(1) // K8s defaults to 1 when unset
		if deploy.Spec.Replicas != nil {
			total = *deploy.Spec.Replicas
		}

		// Get status summary from cache for detailed issue reporting
		statusSummary := ""
		statusIssue := ""
		if resourceStatus := b.provider.GetResourceStatus("Deployment", deploy.Namespace, deploy.Name); resourceStatus != nil {
			statusSummary = resourceStatus.Summary
			statusIssue = resourceStatus.Issue
		}

		nodes = append(nodes, Node{
			ID:     deployID,
			Kind:   KindDeployment,
			Name:   deploy.Name,
			Status: healthLevelToStatus(health.Workload(deploy, time.Now()).Level),
			Data: map[string]any{
				"namespace":     deploy.Namespace,
				"readyReplicas": ready,
				"totalReplicas": total,
				"strategy":      string(deploy.Spec.Strategy.Type),
				"labels":        deploy.Labels,
				"statusSummary": statusSummary,
				"statusIssue":   statusIssue,
			},
		})

		// Track ConfigMap/Secret/PVC references
		refs := extractWorkloadReferences(deploy.Spec.Template.Spec)
		trackWorkloadRefs(deployID, deploy.Namespace, refs)
	}

	// 1b. Add Argo Rollout nodes (CRD - fetched via dynamic cache)
	dynamicCache := b.dynamic
	resourceDiscovery := b.dynamic

	var rolloutGVR schema.GroupVersionResource
	hasRollouts := false
	rolloutsByNamespace := make(map[string][]*unstructured.Unstructured)
	if resourceDiscovery != nil {
		rolloutGVR, hasRollouts = resourceDiscovery.GetGVR("Rollout")
	}
	if hasRollouts && dynamicCache != nil {
		rollouts, err := dynamicCache.ListNamespaces(rolloutGVR, opts.Namespaces)
		if err != nil {
			log.Printf("WARNING [topology] Failed to list Rollouts: %v", err)
			warnings = append(warnings, fmt.Sprintf("Failed to list Rollouts: %v", err))
		}
		for _, rollout := range rollouts {
			ns := rollout.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			rolloutsByNamespace[ns] = append(rolloutsByNamespace[ns], rollout)
			name := rollout.GetName()

			rolloutID := fmt.Sprintf("rollout/%s/%s", ns, name)
			rolloutIDs[ns+"/"+name] = rolloutID

			// Extract status fields
			status, _, _ := unstructured.NestedMap(rollout.Object, "status")
			spec, _, _ := unstructured.NestedMap(rollout.Object, "spec")

			var ready, total int64
			if status != nil {
				ready, _, _ = unstructured.NestedInt64(status, "readyReplicas")
				total, _, _ = unstructured.NestedInt64(status, "replicas")
			}
			if total == 0 && spec != nil {
				total, _, _ = unstructured.NestedInt64(spec, "replicas")
			}

			// Get strategy type
			strategy := "unknown"
			if spec != nil {
				if _, ok, _ := unstructured.NestedMap(spec, "strategy", "canary"); ok {
					strategy = "Canary"
				} else if _, ok, _ := unstructured.NestedMap(spec, "strategy", "blueGreen"); ok {
					strategy = "BlueGreen"
				}
			}

			nodes = append(nodes, Node{
				ID:     rolloutID,
				Kind:   "Rollout",
				Name:   name,
				Status: getDeploymentStatus(int32(ready), int32(total)),
				Data: map[string]any{
					"namespace":     ns,
					"readyReplicas": ready,
					"totalReplicas": total,
					"strategy":      strategy,
					"labels":        rollout.GetLabels(),
					"apiVersion":    rollout.GetAPIVersion(),
				},
			})

			// Extract pod template spec for config references
			template, _, _ := unstructured.NestedMap(spec, "template", "spec")
			if template != nil {
				refs := extractWorkloadReferencesFromMap(template)
				trackWorkloadRefs(rolloutID, ns, refs)
			}
		}
	}

	// 1c. Add ArgoCD Application nodes (CRD - fetched via dynamic cache)
	// Note: Application edges are created in a second pass after all resource IDs are populated
	var applicationGVR schema.GroupVersionResource
	hasApplications := false
	if resourceDiscovery != nil {
		applicationGVR, hasApplications = resourceDiscovery.GetGVRWithGroup("Application", "argoproj.io")
	}
	applicationIDs := make(map[string]string)             // ns/name -> applicationID
	var applicationResources []*unstructured.Unstructured // Store for second pass
	applicationDestNamespaces := make(map[string]string)  // appID -> destNamespace
	if hasApplications && dynamicCache != nil {
		applications, err := dynamicCache.ListNamespaces(applicationGVR, opts.Namespaces)
		if err != nil {
			log.Printf("WARNING [topology] Failed to list ArgoCD Applications: %v", err)
			warnings = append(warnings, fmt.Sprintf("Failed to list ArgoCD Applications: %v", err))
		}
		for _, app := range applications {
			ns := app.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := app.GetName()

			appID := fmt.Sprintf("application/%s/%s", ns, name)
			applicationIDs[ns+"/"+name] = appID

			// Extract status fields
			status, _, _ := unstructured.NestedMap(app.Object, "status")
			spec, _, _ := unstructured.NestedMap(app.Object, "spec")

			// Get sync and health status
			syncStatus := "Unknown"
			healthStatus := "Unknown"
			if status != nil {
				if sync, ok, _ := unstructured.NestedMap(status, "sync"); ok && sync != nil {
					if s, ok := sync["status"].(string); ok {
						syncStatus = s
					}
				}
				if health, ok, _ := unstructured.NestedMap(status, "health"); ok && health != nil {
					if h, ok := health["status"].(string); ok {
						healthStatus = h
					}
				}
			}

			// Map to topology status
			var nodeStatus HealthStatus
			switch healthStatus {
			case "Healthy":
				nodeStatus = StatusHealthy
			case "Progressing":
				nodeStatus = StatusDegraded
			case "Degraded", "Missing":
				nodeStatus = StatusUnhealthy
			default:
				nodeStatus = StatusUnknown
			}

			// Get destination info
			destination := ""
			destNamespace := ""
			if spec != nil {
				if dest, ok, _ := unstructured.NestedMap(spec, "destination"); ok && dest != nil {
					if server, ok := dest["server"].(string); ok {
						destination = server
					} else if name, ok := dest["name"].(string); ok {
						destination = name
					}
					if ns, ok := dest["namespace"].(string); ok {
						destNamespace = ns
					}
				}
			}

			nodes = append(nodes, Node{
				ID:     appID,
				Kind:   KindApplication,
				Name:   name,
				Status: nodeStatus,
				Data: map[string]any{
					"namespace":     ns,
					"syncStatus":    syncStatus,
					"healthStatus":  healthStatus,
					"destination":   destination,
					"destNamespace": destNamespace,
					"labels":        app.GetLabels(),
					"apiVersion":    app.GetAPIVersion(),
				},
			})

			// Store for second pass edge creation
			applicationResources = append(applicationResources, app)
			applicationDestNamespaces[appID] = destNamespace
		}
	}

	// 1d. Add FluxCD Kustomization nodes (CRD - fetched via dynamic cache)
	// Note: Kustomization edges are created in a second pass after all resource IDs are populated
	var kustomizationGVR schema.GroupVersionResource
	hasKustomizations := false
	if resourceDiscovery != nil {
		kustomizationGVR, hasKustomizations = resourceDiscovery.GetGVR("Kustomization")
	}
	kustomizationIDs := make(map[string]string)             // ns/name -> kustomizationID
	var kustomizationResources []*unstructured.Unstructured // Store for second pass
	if hasKustomizations && dynamicCache != nil {
		kustomizations, err := dynamicCache.ListNamespaces(kustomizationGVR, opts.Namespaces)
		if err != nil {
			log.Printf("WARNING [topology] Failed to list FluxCD Kustomizations: %v", err)
			warnings = append(warnings, fmt.Sprintf("Failed to list FluxCD Kustomizations: %v", err))
		}
		for _, ks := range kustomizations {
			ns := ks.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := ks.GetName()

			ksID := fmt.Sprintf("kustomization/%s/%s", ns, name)
			kustomizationIDs[ns+"/"+name] = ksID

			// Extract status fields
			status, _, _ := unstructured.NestedMap(ks.Object, "status")

			// Get ready condition
			readyStatus, nodeStatus := getFluxReadyStatus(status)

			// Get inventory count
			resourceCount := 0
			if status != nil {
				if inventory, ok, _ := unstructured.NestedSlice(status, "inventory", "entries"); ok {
					resourceCount = len(inventory)
				}
			}

			// Get source reference
			sourceRef := ""
			spec, _, _ := unstructured.NestedMap(ks.Object, "spec")
			if spec != nil {
				if ref, ok, _ := unstructured.NestedMap(spec, "sourceRef"); ok && ref != nil {
					kind := ref["kind"]
					refName := ref["name"]
					if kind != nil && refName != nil {
						sourceRef = fmt.Sprintf("%s/%s", kind, refName)
					}
				}
			}

			nodes = append(nodes, Node{
				ID:     ksID,
				Kind:   KindKustomization,
				Name:   name,
				Status: nodeStatus,
				Data: map[string]any{
					"namespace":     ns,
					"ready":         readyStatus,
					"resourceCount": resourceCount,
					"sourceRef":     sourceRef,
					"labels":        ks.GetLabels(),
					"apiVersion":    ks.GetAPIVersion(),
				},
			})

			// Store for second pass edge creation
			kustomizationResources = append(kustomizationResources, ks)
		}
	}

	// 1e. Add FluxCD GitRepository nodes (CRD - fetched via dynamic cache)
	var gitRepoGVR schema.GroupVersionResource
	hasGitRepos := false
	if resourceDiscovery != nil {
		gitRepoGVR, hasGitRepos = resourceDiscovery.GetGVR("GitRepository")
	}
	gitRepoIDs := make(map[string]string) // ns/name -> gitRepoID
	if hasGitRepos && dynamicCache != nil {
		gitRepos, err := dynamicCache.ListNamespaces(gitRepoGVR, opts.Namespaces)
		if err != nil {
			log.Printf("WARNING [topology] Failed to list FluxCD GitRepositories: %v", err)
			warnings = append(warnings, fmt.Sprintf("Failed to list FluxCD GitRepositories: %v", err))
		}
		for _, repo := range gitRepos {
			ns := repo.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := repo.GetName()

			repoID := fmt.Sprintf("gitrepository/%s/%s", ns, name)
			gitRepoIDs[ns+"/"+name] = repoID

			// Extract status fields
			status, _, _ := unstructured.NestedMap(repo.Object, "status")

			// Get ready condition
			readyStatus, nodeStatus := getFluxReadyStatus(status)

			// Get branch from spec
			branch := ""
			spec, _, _ := unstructured.NestedMap(repo.Object, "spec")
			if spec != nil {
				if ref, ok, _ := unstructured.NestedMap(spec, "ref"); ok && ref != nil {
					if b, ok := ref["branch"].(string); ok {
						branch = b
					}
				}
			}

			// Get URL
			url := ""
			if spec != nil {
				if u, ok := spec["url"].(string); ok {
					url = u
				}
			}

			nodes = append(nodes, Node{
				ID:     repoID,
				Kind:   KindGitRepository,
				Name:   name,
				Status: nodeStatus,
				Data: map[string]any{
					"namespace":  ns,
					"ready":      readyStatus,
					"branch":     branch,
					"url":        url,
					"labels":     repo.GetLabels(),
					"apiVersion": repo.GetAPIVersion(),
				},
			})
		}
	}

	// 1f. Add FluxCD HelmRelease nodes (CRD - fetched via dynamic cache)
	var helmReleaseGVR schema.GroupVersionResource
	hasHelmReleases := false
	if resourceDiscovery != nil {
		helmReleaseGVR, hasHelmReleases = resourceDiscovery.GetGVR("HelmRelease")
	}
	helmReleaseIDs := make(map[string]string) // ns/name -> helmReleaseID
	if hasHelmReleases && dynamicCache != nil {
		helmReleases, err := dynamicCache.ListNamespaces(helmReleaseGVR, opts.Namespaces)
		if err != nil {
			log.Printf("WARNING [topology] Failed to list FluxCD HelmReleases: %v", err)
			warnings = append(warnings, fmt.Sprintf("Failed to list FluxCD HelmReleases: %v", err))
		}
		for _, hr := range helmReleases {
			ns := hr.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := hr.GetName()

			hrID := fmt.Sprintf("helmrelease/%s/%s", ns, name)
			helmReleaseIDs[ns+"/"+name] = hrID

			// Extract status fields
			status, _, _ := unstructured.NestedMap(hr.Object, "status")

			// Get ready condition
			readyStatus, nodeStatus := getFluxReadyStatus(status)

			// Get last release revision
			revision := 0
			if status != nil {
				if rev, ok, _ := unstructured.NestedInt64(status, "lastReleaseRevision"); ok {
					revision = int(rev)
				}
			}

			// Get chart info
			chartName := ""
			chartVersion := ""
			spec, _, _ := unstructured.NestedMap(hr.Object, "spec")
			if spec != nil {
				if chart, ok, _ := unstructured.NestedMap(spec, "chart"); ok && chart != nil {
					if chartSpec, ok, _ := unstructured.NestedMap(chart, "spec"); ok && chartSpec != nil {
						if n, ok := chartSpec["chart"].(string); ok {
							chartName = n
						}
						if v, ok := chartSpec["version"].(string); ok {
							chartVersion = v
						}
					}
				}
			}

			nodes = append(nodes, Node{
				ID:     hrID,
				Kind:   KindHelmRelease,
				Name:   name,
				Status: nodeStatus,
				Data: map[string]any{
					"namespace":    ns,
					"ready":        readyStatus,
					"revision":     revision,
					"chartName":    chartName,
					"chartVersion": chartVersion,
					"labels":       hr.GetLabels(),
					"apiVersion":   hr.GetAPIVersion(),
				},
			})
		}
	}

	// 1g. Add cert-manager Certificate nodes (CRD - fetched via dynamic cache)
	// Certificates need explicit handling because Certificate→Secret uses spec.secretName (not ownerRef)
	var certificateGVR schema.GroupVersionResource
	hasCertificates := false
	if resourceDiscovery != nil {
		certificateGVR, hasCertificates = resourceDiscovery.GetGVR("Certificate")
	}
	var certificateResources []unstructured.Unstructured
	if hasCertificates && dynamicCache != nil {
		certs, certErr := dynamicCache.ListNamespaces(certificateGVR, opts.Namespaces)
		if certErr != nil {
			log.Printf("WARNING [topology] Failed to list cert-manager Certificates: %v", certErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list cert-manager Certificates: %v", certErr))
		}
		for _, cert := range certs {
			ns := cert.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := cert.GetName()

			certID := fmt.Sprintf("certificate/%s/%s", ns, name)
			nodes = append(nodes, Node{
				ID:     certID,
				Kind:   KindCertificate,
				Name:   name,
				Status: extractCertificateStatus(*cert),
				Data: map[string]any{
					"namespace":  ns,
					"labels":     cert.GetLabels(),
					"apiVersion": cert.GetAPIVersion(),
				},
			})
			certificateResources = append(certificateResources, *cert)
		}
	}

	// 1h. Add Karpenter NodePool and NodeClaim nodes (CRD - fetched via dynamic cache)
	nodePoolIDs := make(map[string]string)        // ns/name -> nodePoolID
	nodeClaimNodeNames := make(map[string]string) // nodeName -> nodeClaimID (for NodeClaim → Node edges)

	var nodePoolGVR schema.GroupVersionResource
	hasNodePools := false
	if resourceDiscovery != nil {
		nodePoolGVR, hasNodePools = resourceDiscovery.GetGVR("NodePool")
	}
	var cachedNodePools []*unstructured.Unstructured // reused for NodePool→NodeClass edges
	if hasNodePools && dynamicCache != nil {
		nodePools, npErr := dynamicCache.ListNamespaces(nodePoolGVR, opts.Namespaces)
		if npErr != nil {
			log.Printf("WARNING [topology] Failed to list Karpenter NodePools: %v", npErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list Karpenter NodePools: %v", npErr))
		}
		cachedNodePools = nodePools
		for _, np := range nodePools {
			ns := np.GetNamespace()
			if ns != "" && !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := np.GetName()

			npID := fmt.Sprintf("nodepool/%s/%s", ns, name)
			nodePoolIDs[ns+"/"+name] = npID
			nodes = append(nodes, Node{
				ID:     npID,
				Kind:   KindNodePool,
				Name:   name,
				Status: extractKarpenterNodePoolStatus(*np),
				Data: map[string]any{
					"namespace":  ns,
					"labels":     np.GetLabels(),
					"apiVersion": np.GetAPIVersion(),
				},
			})
		}
	}

	var nodeClaimGVR schema.GroupVersionResource
	hasNodeClaims := false
	if resourceDiscovery != nil {
		nodeClaimGVR, hasNodeClaims = resourceDiscovery.GetGVR("NodeClaim")
	}
	if hasNodeClaims && dynamicCache != nil {
		nodeClaims, ncErr := dynamicCache.ListNamespaces(nodeClaimGVR, opts.Namespaces)
		if ncErr != nil {
			log.Printf("WARNING [topology] Failed to list Karpenter NodeClaims: %v", ncErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list Karpenter NodeClaims: %v", ncErr))
		}
		for _, nc := range nodeClaims {
			ns := nc.GetNamespace()
			if ns != "" && !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := nc.GetName()

			ncID := fmt.Sprintf("nodeclaim/%s/%s", ns, name)
			nodes = append(nodes, Node{
				ID:     ncID,
				Kind:   KindNodeClaim,
				Name:   name,
				Status: extractKarpenterNodeClaimStatus(*nc),
				Data: map[string]any{
					"namespace":  ns,
					"labels":     nc.GetLabels(),
					"apiVersion": nc.GetAPIVersion(),
				},
			})

			// NodePool → NodeClaim edge via ownerRef or karpenter.sh/nodepool label
			edgeAdded := false
			for _, ownerRef := range nc.GetOwnerReferences() {
				if ownerRef.Kind == "NodePool" {
					// NodePool is cluster-scoped, so key uses empty namespace
					if ownerID, ok := nodePoolIDs["/"+ownerRef.Name]; ok {
						edges = append(edges, Edge{
							ID:     fmt.Sprintf("%s-to-%s", ownerID, ncID),
							Source: ownerID,
							Target: ncID,
							Type:   EdgeManages,
						})
						edgeAdded = true
					}
				}
			}
			// Fallback: use karpenter.sh/nodepool label if no ownerRef matched
			if !edgeAdded {
				if poolName, ok := nc.GetLabels()["karpenter.sh/nodepool"]; ok {
					if ownerID, ok := nodePoolIDs["/"+poolName]; ok {
						edges = append(edges, Edge{
							ID:     fmt.Sprintf("%s-to-%s", ownerID, ncID),
							Source: ownerID,
							Target: ncID,
							Type:   EdgeManages,
						})
					}
				}
			}

			// Collect status.nodeName for NodeClaim → Node edges
			if nodeName, _, _ := unstructured.NestedString(nc.Object, "status", "nodeName"); nodeName != "" {
				nodeClaimNodeNames[nodeName] = ncID
			}

		}
	}

	// 1h-ii-a. Add Karpenter-managed Node nodes (NodeClaim → Node edges)
	if len(nodeClaimNodeNames) > 0 {
		allNodes, nodeErr := b.provider.Nodes()
		if nodeErr != nil {
			log.Printf("WARNING [topology] Failed to list Nodes for Karpenter edges: %v", nodeErr)
		} else {
			for _, node := range allNodes {
				ncID, ok := nodeClaimNodeNames[node.Name]
				if !ok {
					continue // skip non-Karpenter nodes
				}
				nodeID := fmt.Sprintf("node//%s", node.Name)
				nodes = append(nodes, Node{
					ID:     nodeID,
					Kind:   KindNode,
					Name:   node.Name,
					Status: extractNodeStatus(*node),
					Data: map[string]any{
						"namespace":    "",
						"labels":       node.Labels,
						"instanceType": node.Labels["node.kubernetes.io/instance-type"],
					},
				})
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", ncID, nodeID),
					Source: ncID,
					Target: nodeID,
					Type:   EdgeManages,
				})
			}
		}
	}

	// 1h-iii. Add Karpenter NodeClass nodes (EC2NodeClass, AKSNodeClass, etc.)
	nodeClassIDs := make(map[string]string) // "kind/name" -> nodeClassID (cluster-scoped, keyed by kind to avoid collision)

	// Try common NodeClass kinds across cloud providers
	nodeClassKinds := []string{"EC2NodeClass", "AKSNodeClass", "GCENodeClass"}
	for _, ncKind := range nodeClassKinds {
		var ncGVR schema.GroupVersionResource
		var hasKind bool
		if resourceDiscovery != nil {
			ncGVR, hasKind = resourceDiscovery.GetGVR(ncKind)
		}
		if !hasKind || dynamicCache == nil {
			continue
		}
		nodeClasses, ncErr := dynamicCache.List(ncGVR, "")
		if ncErr != nil {
			log.Printf("WARNING [topology] Failed to list Karpenter %s: %v", ncKind, ncErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list Karpenter %s: %v", ncKind, ncErr))
			continue
		}
		for _, nc := range nodeClasses {
			name := nc.GetName()
			ncID := fmt.Sprintf("nodeclass//%s", name)
			nodeClassIDs[ncKind+"/"+name] = ncID
			nodes = append(nodes, Node{
				ID:     ncID,
				Kind:   KindNodeClass,
				Name:   name,
				Status: extractKarpenterNodePoolStatus(*nc), // Same Ready condition pattern
				Data: map[string]any{
					"namespace":  "",
					"labels":     nc.GetLabels(),
					"apiVersion": nc.GetAPIVersion(),
				},
			})
		}
	}

	// NodePool → NodeClass edges via spec.template.spec.nodeClassRef
	if len(nodeClassIDs) > 0 {
		for _, np := range cachedNodePools {
			npNs := np.GetNamespace()
			npName := np.GetName()
			npID, ok := nodePoolIDs[npNs+"/"+npName]
			if !ok {
				continue
			}
			refName, _, _ := unstructured.NestedString(np.Object, "spec", "template", "spec", "nodeClassRef", "name")
			refKind, _, _ := unstructured.NestedString(np.Object, "spec", "template", "spec", "nodeClassRef", "kind")
			if refName != "" && refKind != "" {
				if ncID, ok := nodeClassIDs[refKind+"/"+refName]; ok {
					edges = append(edges, Edge{
						ID:     fmt.Sprintf("%s-to-%s", npID, ncID),
						Source: npID,
						Target: ncID,
						Type:   EdgeConfigures,
					})
				}
			}
		}
	}

	// 1i. Add KEDA ScaledObject and ScaledJob nodes (CRD - fetched via dynamic cache)
	var scaledObjectGVR schema.GroupVersionResource
	hasScaledObjects := false
	if resourceDiscovery != nil {
		scaledObjectGVR, hasScaledObjects = resourceDiscovery.GetGVR("ScaledObject")
	}
	if hasScaledObjects && dynamicCache != nil {
		scaledObjects, soErr := dynamicCache.ListNamespaces(scaledObjectGVR, opts.Namespaces)
		if soErr != nil {
			log.Printf("WARNING [topology] Failed to list KEDA ScaledObjects: %v", soErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list KEDA ScaledObjects: %v", soErr))
		}
		for _, so := range scaledObjects {
			ns := so.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := so.GetName()

			soID := fmt.Sprintf("scaledobject/%s/%s", ns, name)
			nodes = append(nodes, Node{
				ID:     soID,
				Kind:   KindScaledObject,
				Name:   name,
				Status: extractKedaScaledObjectStatus(*so),
				Data: map[string]any{
					"namespace":  ns,
					"labels":     so.GetLabels(),
					"apiVersion": so.GetAPIVersion(),
				},
			})

			// ScaledObject → target workload edge (via spec.scaleTargetRef)
			targetKind, _, _ := unstructured.NestedString(so.Object, "spec", "scaleTargetRef", "kind")
			targetName, _, _ := unstructured.NestedString(so.Object, "spec", "scaleTargetRef", "name")
			if targetKind == "" {
				targetKind = "Deployment" // KEDA defaults to Deployment when kind is omitted
			}
			if targetName != "" {
				targetKey := ns + "/" + targetName
				var targetID string
				switch targetKind {
				case "Deployment":
					targetID = deploymentIDs[targetKey]
				case "StatefulSet":
					targetID = statefulSetIDs[targetKey]
				case "Rollout":
					targetID = rolloutIDs[targetKey]
				}
				if targetID != "" {
					edges = append(edges, Edge{
						ID:     fmt.Sprintf("%s-to-%s", soID, targetID),
						Source: soID,
						Target: targetID,
						Type:   EdgeUses,
					})
				}
			}
		}
	}

	var scaledJobGVR schema.GroupVersionResource
	hasScaledJobs := false
	if resourceDiscovery != nil {
		scaledJobGVR, hasScaledJobs = resourceDiscovery.GetGVR("ScaledJob")
	}
	if hasScaledJobs && dynamicCache != nil {
		scaledJobs, sjErr := dynamicCache.ListNamespaces(scaledJobGVR, opts.Namespaces)
		if sjErr != nil {
			log.Printf("WARNING [topology] Failed to list KEDA ScaledJobs: %v", sjErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list KEDA ScaledJobs: %v", sjErr))
		}
		for _, sj := range scaledJobs {
			ns := sj.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := sj.GetName()

			sjID := fmt.Sprintf("scaledjob/%s/%s", ns, name)
			scaledJobIDs[ns+"/"+name] = sjID
			nodes = append(nodes, Node{
				ID:     sjID,
				Kind:   KindScaledJob,
				Name:   name,
				Status: extractKedaScaledJobStatus(*sj),
				Data: map[string]any{
					"namespace":  ns,
					"labels":     sj.GetLabels(),
					"apiVersion": sj.GetAPIVersion(),
				},
			})
		}
	}

	// 1i-b. Add Cluster API (CAPI) nodes and edges
	capiClusterIDs := make(map[string]string) // ns/name -> clusterID
	var cachedCAPIClusters []*unstructured.Unstructured

	var capiClusterGVR schema.GroupVersionResource
	hasCAPIClusters := false
	if resourceDiscovery != nil {
		capiClusterGVR, hasCAPIClusters = resourceDiscovery.GetGVRWithGroup("Cluster", "cluster.x-k8s.io")
	}
	if hasCAPIClusters && dynamicCache != nil {
		clusters, clErr := dynamicCache.ListNamespaces(capiClusterGVR, opts.Namespaces)
		if clErr != nil {
			log.Printf("WARNING [topology] Failed to list CAPI Clusters: %v", clErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list CAPI Clusters: %v", clErr))
		}
		cachedCAPIClusters = clusters
		for _, cl := range clusters {
			ns := cl.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := cl.GetName()
			clID := fmt.Sprintf("capicluster/%s/%s", ns, name)
			capiClusterIDs[ns+"/"+name] = clID
			nodes = append(nodes, Node{
				ID:     clID,
				Kind:   KindCAPICluster,
				Name:   name,
				Status: extractCAPIPhaseStatus(*cl),
				Data: map[string]any{
					"namespace":  ns,
					"labels":     cl.GetLabels(),
					"apiVersion": cl.GetAPIVersion(),
				},
			})
		}
	}

	// CAPI ClusterClass nodes + ClusterClass → Cluster edges
	clusterClassIDs := make(map[string]string) // name -> classID (cluster-scoped)
	var capiClusterClassGVR schema.GroupVersionResource
	hasCAPIClusterClasses := false
	if resourceDiscovery != nil {
		capiClusterClassGVR, hasCAPIClusterClasses = resourceDiscovery.GetGVR("ClusterClass")
	}
	if hasCAPIClusterClasses && dynamicCache != nil {
		clusterClasses, ccErr := dynamicCache.ListNamespaces(capiClusterClassGVR, opts.Namespaces)
		if ccErr != nil {
			log.Printf("WARNING [topology] Failed to list CAPI ClusterClasses: %v", ccErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list CAPI ClusterClasses: %v", ccErr))
		}
		for _, cc := range clusterClasses {
			ns := cc.GetNamespace()
			if ns != "" && !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := cc.GetName()
			ccID := fmt.Sprintf("clusterclass/%s/%s", ns, name)
			clusterClassIDs[ns+"/"+name] = ccID
			nodes = append(nodes, Node{
				ID:     ccID,
				Kind:   KindClusterClass,
				Name:   name,
				Status: extractCAPIReadyConditionStatus(*cc),
				Data: map[string]any{
					"namespace":  ns,
					"labels":     cc.GetLabels(),
					"apiVersion": cc.GetAPIVersion(),
				},
			})
		}

		// ClusterClass → Cluster edges (via spec.topology.class)
		if len(clusterClassIDs) > 0 {
			for _, cl := range cachedCAPIClusters {
				ns := cl.GetNamespace()
				if !opts.MatchesNamespaceFilter(ns) {
					continue
				}
				clID, ok := capiClusterIDs[ns+"/"+cl.GetName()]
				if !ok {
					continue
				}
				className, _, _ := unstructured.NestedString(cl.Object, "spec", "topology", "class")
				if className == "" {
					continue
				}
				// ClusterClass can be in same namespace or cluster-scoped
				ccID, ok := clusterClassIDs[ns+"/"+className]
				if !ok {
					ccID, ok = clusterClassIDs["/"+className]
				}
				if ok {
					edges = append(edges, Edge{
						ID:     fmt.Sprintf("%s-to-%s", ccID, clID),
						Source: ccID,
						Target: clID,
						Type:   EdgeConfigures,
					})
				}
			}
		}
	}

	// CAPI KubeadmControlPlane nodes + Cluster → KCP edges
	kcpIDs := make(map[string]string) // ns/name -> kcpID
	var kcpGVR schema.GroupVersionResource
	hasKCPs := false
	if resourceDiscovery != nil {
		kcpGVR, hasKCPs = resourceDiscovery.GetGVR("KubeadmControlPlane")
	}
	if hasKCPs && dynamicCache != nil {
		kcps, kcpErr := dynamicCache.ListNamespaces(kcpGVR, opts.Namespaces)
		if kcpErr != nil {
			log.Printf("WARNING [topology] Failed to list CAPI KubeadmControlPlanes: %v", kcpErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list CAPI KubeadmControlPlanes: %v", kcpErr))
		}
		for _, kcp := range kcps {
			ns := kcp.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := kcp.GetName()
			kcpID := fmt.Sprintf("kubeadmcontrolplane/%s/%s", ns, name)
			kcpIDs[ns+"/"+name] = kcpID
			nodes = append(nodes, Node{
				ID:     kcpID,
				Kind:   KindKubeadmControlPlane,
				Name:   name,
				Status: extractCAPIReadyConditionStatus(*kcp),
				Data: map[string]any{
					"namespace":  ns,
					"labels":     kcp.GetLabels(),
					"apiVersion": kcp.GetAPIVersion(),
				},
			})

			// Cluster → KCP edge via ownerRef
			for _, ownerRef := range kcp.GetOwnerReferences() {
				if ownerRef.Kind == "Cluster" {
					if clID, ok := capiClusterIDs[ns+"/"+ownerRef.Name]; ok {
						edges = append(edges, Edge{
							ID:     fmt.Sprintf("%s-to-%s", clID, kcpID),
							Source: clID,
							Target: kcpID,
							Type:   EdgeManages,
						})
					}
				}
			}
		}
	}

	// CAPI MachineDeployment nodes + Cluster → MD edges
	machineDeploymentIDs := make(map[string]string) // ns/name -> mdID
	var mdGVR schema.GroupVersionResource
	hasMDs := false
	if resourceDiscovery != nil {
		mdGVR, hasMDs = resourceDiscovery.GetGVR("MachineDeployment")
	}
	if hasMDs && dynamicCache != nil {
		mds, mdErr := dynamicCache.ListNamespaces(mdGVR, opts.Namespaces)
		if mdErr != nil {
			log.Printf("WARNING [topology] Failed to list CAPI MachineDeployments: %v", mdErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list CAPI MachineDeployments: %v", mdErr))
		}
		for _, md := range mds {
			ns := md.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := md.GetName()
			mdID := fmt.Sprintf("machinedeployment/%s/%s", ns, name)
			machineDeploymentIDs[ns+"/"+name] = mdID
			nodes = append(nodes, Node{
				ID:     mdID,
				Kind:   KindMachineDeployment,
				Name:   name,
				Status: extractCAPIPhaseStatus(*md),
				Data: map[string]any{
					"namespace":  ns,
					"labels":     md.GetLabels(),
					"apiVersion": md.GetAPIVersion(),
				},
			})

			// Cluster → MachineDeployment edge via ownerRef
			for _, ownerRef := range md.GetOwnerReferences() {
				if ownerRef.Kind == "Cluster" {
					if clID, ok := capiClusterIDs[ns+"/"+ownerRef.Name]; ok {
						edges = append(edges, Edge{
							ID:     fmt.Sprintf("%s-to-%s", clID, mdID),
							Source: clID,
							Target: mdID,
							Type:   EdgeManages,
						})
					}
				}
			}
		}
	}

	// CAPI MachinePool nodes + Cluster → MP edges
	machinePoolIDs := make(map[string]string) // ns/name -> mpID
	var mpGVR schema.GroupVersionResource
	hasMPs := false
	if resourceDiscovery != nil {
		mpGVR, hasMPs = resourceDiscovery.GetGVR("MachinePool")
	}
	if hasMPs && dynamicCache != nil {
		mps, mpErr := dynamicCache.ListNamespaces(mpGVR, opts.Namespaces)
		if mpErr != nil {
			log.Printf("WARNING [topology] Failed to list CAPI MachinePools: %v", mpErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list CAPI MachinePools: %v", mpErr))
		}
		for _, mp := range mps {
			ns := mp.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := mp.GetName()
			mpID := fmt.Sprintf("machinepool/%s/%s", ns, name)
			machinePoolIDs[ns+"/"+name] = mpID
			nodes = append(nodes, Node{
				ID:     mpID,
				Kind:   KindMachinePool,
				Name:   name,
				Status: extractCAPIPhaseStatus(*mp),
				Data: map[string]any{
					"namespace":  ns,
					"labels":     mp.GetLabels(),
					"apiVersion": mp.GetAPIVersion(),
				},
			})

			// Cluster → MachinePool edge via ownerRef
			for _, ownerRef := range mp.GetOwnerReferences() {
				if ownerRef.Kind == "Cluster" {
					if clID, ok := capiClusterIDs[ns+"/"+ownerRef.Name]; ok {
						edges = append(edges, Edge{
							ID:     fmt.Sprintf("%s-to-%s", clID, mpID),
							Source: clID,
							Target: mpID,
							Type:   EdgeManages,
						})
					}
				}
			}
		}
	}

	// CAPI MachineSet nodes + MachineDeployment → MS edges
	capiMachineSetIDs := make(map[string]string) // ns/name -> msID
	var capiMsGVR schema.GroupVersionResource
	hasCAPIMachineSets := false
	if resourceDiscovery != nil {
		capiMsGVR, hasCAPIMachineSets = resourceDiscovery.GetGVRWithGroup("MachineSet", "cluster.x-k8s.io")
	}
	if hasCAPIMachineSets && dynamicCache != nil {
		machineSets, msErr := dynamicCache.ListNamespaces(capiMsGVR, opts.Namespaces)
		if msErr != nil {
			log.Printf("WARNING [topology] Failed to list CAPI MachineSets: %v", msErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list CAPI MachineSets: %v", msErr))
		}
		for _, ms := range machineSets {
			ns := ms.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := ms.GetName()
			msID := fmt.Sprintf("machineset/%s/%s", ns, name)
			capiMachineSetIDs[ns+"/"+name] = msID
			nodes = append(nodes, Node{
				ID:     msID,
				Kind:   KindMachineSet,
				Name:   name,
				Status: extractCAPIPhaseStatus(*ms),
				Data: map[string]any{
					"namespace":  ns,
					"labels":     ms.GetLabels(),
					"apiVersion": ms.GetAPIVersion(),
				},
			})

			// MachineDeployment → MachineSet edge via ownerRef
			for _, ownerRef := range ms.GetOwnerReferences() {
				if ownerRef.Kind == "MachineDeployment" {
					if mdID, ok := machineDeploymentIDs[ns+"/"+ownerRef.Name]; ok {
						edges = append(edges, Edge{
							ID:     fmt.Sprintf("%s-to-%s", mdID, msID),
							Source: mdID,
							Target: msID,
							Type:   EdgeManages,
						})
					}
				}
			}
		}
	}

	// CAPI Machine nodes + owner edges (MachineSet/KCP/MachinePool → Machine) + Machine → Node edge
	capiMachineNodeNames := make(map[string]string) // nodeName -> machineID (for Machine → Node edges)
	var capiMachineGVR schema.GroupVersionResource
	hasCAPIMachines := false
	if resourceDiscovery != nil {
		capiMachineGVR, hasCAPIMachines = resourceDiscovery.GetGVRWithGroup("Machine", "cluster.x-k8s.io")
	}
	if hasCAPIMachines && dynamicCache != nil {
		machines, mErr := dynamicCache.ListNamespaces(capiMachineGVR, opts.Namespaces)
		if mErr != nil {
			log.Printf("WARNING [topology] Failed to list CAPI Machines: %v", mErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list CAPI Machines: %v", mErr))
		}
		for _, m := range machines {
			ns := m.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := m.GetName()
			mID := fmt.Sprintf("machine/%s/%s", ns, name)
			nodes = append(nodes, Node{
				ID:     mID,
				Kind:   KindMachine,
				Name:   name,
				Status: extractCAPIPhaseStatus(*m),
				Data: map[string]any{
					"namespace":  ns,
					"labels":     m.GetLabels(),
					"apiVersion": m.GetAPIVersion(),
				},
			})

			// Owner edges: MachineSet, KCP, or MachinePool → Machine
			for _, ownerRef := range m.GetOwnerReferences() {
				var ownerID string
				switch ownerRef.Kind {
				case "MachineSet":
					ownerID = capiMachineSetIDs[ns+"/"+ownerRef.Name]
				case "KubeadmControlPlane":
					ownerID = kcpIDs[ns+"/"+ownerRef.Name]
				case "MachinePool":
					ownerID = machinePoolIDs[ns+"/"+ownerRef.Name]
				}
				if ownerID != "" {
					edges = append(edges, Edge{
						ID:     fmt.Sprintf("%s-to-%s", ownerID, mID),
						Source: ownerID,
						Target: mID,
						Type:   EdgeManages,
					})
				}
			}

			// Collect status.nodeRef.name for Machine → Node edges
			if nodeName, _, _ := unstructured.NestedString(m.Object, "status", "nodeRef", "name"); nodeName != "" {
				capiMachineNodeNames[nodeName] = mID
			}
		}
	}

	// CAPI Machine → Node edges (via status.nodeRef)
	if len(capiMachineNodeNames) > 0 {
		allNodes, nodeErr := b.provider.Nodes()
		if nodeErr != nil {
			log.Printf("WARNING [topology] Failed to list Nodes for CAPI Machine edges: %v", nodeErr)
		} else {
			for _, node := range allNodes {
				machineID, ok := capiMachineNodeNames[node.Name]
				if !ok {
					continue
				}
				nodeID := fmt.Sprintf("node//%s", node.Name)
				// Only add node if not already present (Karpenter may have added it via NodeClaim)
				if _, karpenterManaged := nodeClaimNodeNames[node.Name]; !karpenterManaged {
					nodes = append(nodes, Node{
						ID:     nodeID,
						Kind:   KindNode,
						Name:   node.Name,
						Status: extractNodeStatus(*node),
						Data: map[string]any{
							"namespace":    "",
							"labels":       node.Labels,
							"instanceType": node.Labels["node.kubernetes.io/instance-type"],
						},
					})
				}
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", machineID, nodeID),
					Source: machineID,
					Target: nodeID,
					Type:   EdgeManages,
				})
			}
		}
	}

	// CAPI MachineHealthCheck nodes + MHC → target edges
	var mhcGVR schema.GroupVersionResource
	hasMHCs := false
	if resourceDiscovery != nil {
		mhcGVR, hasMHCs = resourceDiscovery.GetGVR("MachineHealthCheck")
	}
	if hasMHCs && dynamicCache != nil {
		mhcs, mhcErr := dynamicCache.ListNamespaces(mhcGVR, opts.Namespaces)
		if mhcErr != nil {
			log.Printf("WARNING [topology] Failed to list CAPI MachineHealthChecks: %v", mhcErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list CAPI MachineHealthChecks: %v", mhcErr))
		}
		for _, mhc := range mhcs {
			ns := mhc.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := mhc.GetName()
			mhcID := fmt.Sprintf("machinehealthcheck/%s/%s", ns, name)
			nodes = append(nodes, Node{
				ID:     mhcID,
				Kind:   KindMachineHealthCheck,
				Name:   name,
				Status: extractCAPIReadyConditionStatus(*mhc),
				Data: map[string]any{
					"namespace":  ns,
					"labels":     mhc.GetLabels(),
					"apiVersion": mhc.GetAPIVersion(),
				},
			})

			// MHC → Cluster edge via ownerRef
			for _, ownerRef := range mhc.GetOwnerReferences() {
				if ownerRef.Kind == "Cluster" {
					if clID, ok := capiClusterIDs[ns+"/"+ownerRef.Name]; ok {
						edges = append(edges, Edge{
							ID:     fmt.Sprintf("%s-to-%s", mhcID, clID),
							Source: mhcID,
							Target: clID,
							Type:   EdgeProtects,
						})
					}
				}
			}
		}
	}

	// 1j. Add Gateway API GatewayClass nodes (CRD - fetched via dynamic cache)
	gatewayClassIDs := make(map[string]string) // name -> gatewayClassID (cluster-scoped)

	var gatewayClassGVR schema.GroupVersionResource
	hasGatewayClasses := false
	if resourceDiscovery != nil {
		gatewayClassGVR, hasGatewayClasses = resourceDiscovery.GetGVR("GatewayClass")
	}
	if hasGatewayClasses && dynamicCache != nil {
		gatewayClasses, gcErr := dynamicCache.List(gatewayClassGVR, "")
		if gcErr != nil {
			log.Printf("WARNING [topology] Failed to list GatewayClasses: %v", gcErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list GatewayClasses: %v", gcErr))
		}
		for _, gc := range gatewayClasses {
			name := gc.GetName()

			gcID := fmt.Sprintf("gatewayclass//%s", name)
			gatewayClassIDs[name] = gcID
			nodes = append(nodes, Node{
				ID:     gcID,
				Kind:   KindGatewayClass,
				Name:   name,
				Status: extractGatewayClassStatus(*gc),
				Data: map[string]any{
					"labels":     gc.GetLabels(),
					"apiVersion": gc.GetAPIVersion(),
				},
			})
		}
	}

	// 1k. Add Istio VirtualService nodes (CRD - fetched via dynamic cache)
	// VirtualService edges are created in a second pass after service IDs are populated
	var virtualServiceGVR schema.GroupVersionResource
	hasVirtualServices := false
	if resourceDiscovery != nil {
		virtualServiceGVR, hasVirtualServices = resourceDiscovery.GetGVRWithGroup("VirtualService", "networking.istio.io")
	}
	virtualServiceIDs := make(map[string]string)             // ns/name -> vsID
	var virtualServiceResources []*unstructured.Unstructured // Store for second pass
	if hasVirtualServices && dynamicCache != nil {
		virtualServices, vsErr := dynamicCache.ListNamespaces(virtualServiceGVR, opts.Namespaces)
		if vsErr != nil {
			log.Printf("WARNING [topology] Failed to list Istio VirtualServices: %v", vsErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list Istio VirtualServices: %v", vsErr))
		}
		for _, vs := range virtualServices {
			ns := vs.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := vs.GetName()

			vsID := fmt.Sprintf("virtualservice/%s/%s", ns, name)
			virtualServiceIDs[ns+"/"+name] = vsID

			// Extract spec fields for display
			spec, _, _ := unstructured.NestedMap(vs.Object, "spec")
			hosts, _, _ := unstructured.NestedStringSlice(spec, "hosts")
			gateways, _, _ := unstructured.NestedStringSlice(spec, "gateways")

			httpRoutes, _, _ := unstructured.NestedSlice(spec, "http")
			tcpRoutes, _, _ := unstructured.NestedSlice(spec, "tcp")
			tlsRoutes, _, _ := unstructured.NestedSlice(spec, "tls")
			routeCount := len(httpRoutes) + len(tcpRoutes) + len(tlsRoutes)

			nodeStatus := StatusHealthy
			if routeCount == 0 {
				nodeStatus = StatusUnhealthy
			}

			nodes = append(nodes, Node{
				ID:     vsID,
				Kind:   KindVirtualService,
				Name:   name,
				Status: nodeStatus,
				Data: map[string]any{
					"namespace":  ns,
					"hosts":      hosts,
					"gateways":   gateways,
					"routeCount": routeCount,
					"labels":     vs.GetLabels(),
					"apiVersion": vs.GetAPIVersion(),
				},
			})

			virtualServiceResources = append(virtualServiceResources, vs)
		}
	}

	// 1l. Add Istio DestinationRule nodes (CRD - fetched via dynamic cache)
	// DestinationRule edges are created in a second pass after service IDs are populated
	var destinationRuleGVR schema.GroupVersionResource
	hasDestinationRules := false
	if resourceDiscovery != nil {
		destinationRuleGVR, hasDestinationRules = resourceDiscovery.GetGVRWithGroup("DestinationRule", "networking.istio.io")
	}
	destinationRuleIDs := make(map[string]string)             // ns/name -> drID
	var destinationRuleResources []*unstructured.Unstructured // Store for second pass
	if hasDestinationRules && dynamicCache != nil {
		destinationRules, drErr := dynamicCache.ListNamespaces(destinationRuleGVR, opts.Namespaces)
		if drErr != nil {
			log.Printf("WARNING [topology] Failed to list Istio DestinationRules: %v", drErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list Istio DestinationRules: %v", drErr))
		}
		for _, dr := range destinationRules {
			ns := dr.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := dr.GetName()

			drID := fmt.Sprintf("destinationrule/%s/%s", ns, name)
			destinationRuleIDs[ns+"/"+name] = drID

			host, _, _ := unstructured.NestedString(dr.Object, "spec", "host")
			subsets, _, _ := unstructured.NestedSlice(dr.Object, "spec", "subsets")

			nodeStatus := StatusHealthy
			if host == "" {
				nodeStatus = StatusUnhealthy
			}

			nodes = append(nodes, Node{
				ID:     drID,
				Kind:   KindDestinationRule,
				Name:   name,
				Status: nodeStatus,
				Data: map[string]any{
					"namespace":   ns,
					"host":        host,
					"subsetCount": len(subsets),
					"labels":      dr.GetLabels(),
					"apiVersion":  dr.GetAPIVersion(),
				},
			})

			destinationRuleResources = append(destinationRuleResources, dr)
		}
	}

	// 1m. Add Istio Gateway nodes (CRD - fetched via dynamic cache)
	// Note: This is Istio's own Gateway (networking.istio.io), NOT Gateway API (gateway.networking.k8s.io)
	var istioGatewayGVR schema.GroupVersionResource
	hasIstioGateways := false
	if resourceDiscovery != nil {
		istioGatewayGVR, hasIstioGateways = resourceDiscovery.GetGVRWithGroup("Gateway", "networking.istio.io")
	}
	istioGatewayIDs := make(map[string]string) // ns/name -> igwID
	if hasIstioGateways && dynamicCache != nil {
		istioGateways, igwErr := dynamicCache.ListNamespaces(istioGatewayGVR, opts.Namespaces)
		if igwErr != nil {
			log.Printf("WARNING [topology] Failed to list Istio Gateways: %v", igwErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list Istio Gateways: %v", igwErr))
		}
		for _, igw := range istioGateways {
			ns := igw.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := igw.GetName()

			igwID := fmt.Sprintf("istiogateway/%s/%s", ns, name)
			istioGatewayIDs[ns+"/"+name] = igwID

			servers, _, _ := unstructured.NestedSlice(igw.Object, "spec", "servers")
			selector, _, _ := unstructured.NestedStringMap(igw.Object, "spec", "selector")

			nodeStatus := StatusHealthy
			if len(servers) == 0 {
				nodeStatus = StatusUnhealthy
			}

			nodes = append(nodes, Node{
				ID:     igwID,
				Kind:   KindIstioGateway,
				Name:   name,
				Status: nodeStatus,
				Data: map[string]any{
					"namespace":   ns,
					"serverCount": len(servers),
					"selector":    selector,
					"labels":      igw.GetLabels(),
					"apiVersion":  igw.GetAPIVersion(),
				},
			})
		}
	}

	// 1n. Add KNative Serving nodes (CRD - fetched via dynamic cache)
	// KnativeService, Configuration, Revision, Route
	// Edges are created in a second pass after all IDs are populated

	// KNative Service (collides with core Service kind, use GetGVRWithGroup)
	var knativeServiceGVR schema.GroupVersionResource
	hasKnativeServices := false
	if resourceDiscovery != nil {
		knativeServiceGVR, hasKnativeServices = resourceDiscovery.GetGVRWithGroup("Service", "serving.knative.dev")
	}
	knativeServiceIDs := make(map[string]string)             // ns/name -> ksvcID
	var knativeServiceResources []*unstructured.Unstructured // Store for second pass
	if hasKnativeServices && dynamicCache != nil {
		knativeServices, ksvcErr := dynamicCache.ListNamespaces(knativeServiceGVR, opts.Namespaces)
		if ksvcErr != nil {
			log.Printf("WARNING [topology] Failed to list KNative Services: %v", ksvcErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list KNative Services: %v", ksvcErr))
		}
		for _, ksvc := range knativeServices {
			ns := ksvc.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := ksvc.GetName()

			ksvcID := fmt.Sprintf("knativeservice/%s/%s", ns, name)
			knativeServiceIDs[ns+"/"+name] = ksvcID

			nodes = append(nodes, Node{
				ID:     ksvcID,
				Kind:   KindKnativeService,
				Name:   name,
				Status: extractGenericStatus(ksvc),
				Data: map[string]any{
					"namespace":  ns,
					"labels":     ksvc.GetLabels(),
					"apiVersion": ksvc.GetAPIVersion(),
				},
			})

			knativeServiceResources = append(knativeServiceResources, ksvc)
		}
	}

	// KNative Configuration (needs group-qualified lookup — "Configuration" could collide with other CRDs)
	var knativeConfigGVR schema.GroupVersionResource
	hasKnativeConfigs := false
	if resourceDiscovery != nil {
		knativeConfigGVR, hasKnativeConfigs = resourceDiscovery.GetGVRWithGroup("Configuration", "serving.knative.dev")
	}
	knativeConfigIDs := make(map[string]string)             // ns/name -> kcfgID
	var knativeConfigResources []*unstructured.Unstructured // Store for edge creation
	if hasKnativeConfigs && dynamicCache != nil {
		knativeConfigs, kcfgErr := dynamicCache.ListNamespaces(knativeConfigGVR, opts.Namespaces)
		if kcfgErr != nil {
			log.Printf("WARNING [topology] Failed to list KNative Configurations: %v", kcfgErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list KNative Configurations: %v", kcfgErr))
		}
		for _, kcfg := range knativeConfigs {
			ns := kcfg.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := kcfg.GetName()

			kcfgID := fmt.Sprintf("knativeconfiguration/%s/%s", ns, name)
			knativeConfigIDs[ns+"/"+name] = kcfgID
			knativeConfigResources = append(knativeConfigResources, kcfg)

			nodes = append(nodes, Node{
				ID:     kcfgID,
				Kind:   KindKnativeConfiguration,
				Name:   name,
				Status: extractGenericStatus(kcfg),
				Data: map[string]any{
					"namespace":  ns,
					"labels":     kcfg.GetLabels(),
					"apiVersion": kcfg.GetAPIVersion(),
				},
			})
		}
	}

	// KNative Revision (no collision, use GetGVR)
	var knativeRevisionGVR schema.GroupVersionResource
	hasKnativeRevisions := false
	if resourceDiscovery != nil {
		knativeRevisionGVR, hasKnativeRevisions = resourceDiscovery.GetGVR("Revision")
	}
	knativeRevisionIDs := make(map[string]string)             // ns/name -> krevID
	var knativeRevisionResources []*unstructured.Unstructured // Store for edge creation
	if hasKnativeRevisions && dynamicCache != nil {
		knativeRevisions, krevErr := dynamicCache.ListNamespaces(knativeRevisionGVR, opts.Namespaces)
		if krevErr != nil {
			log.Printf("WARNING [topology] Failed to list KNative Revisions: %v", krevErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list KNative Revisions: %v", krevErr))
		}
		for _, krev := range knativeRevisions {
			ns := krev.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := krev.GetName()

			krevID := fmt.Sprintf("knativerevision/%s/%s", ns, name)
			knativeRevisionIDs[ns+"/"+name] = krevID
			knativeRevisionResources = append(knativeRevisionResources, krev)

			nodes = append(nodes, Node{
				ID:     krevID,
				Kind:   KindKnativeRevision,
				Name:   name,
				Status: extractGenericStatus(krev),
				Data: map[string]any{
					"namespace":  ns,
					"labels":     krev.GetLabels(),
					"apiVersion": krev.GetAPIVersion(),
				},
			})
		}
	}

	// KNative Route (needs group-qualified lookup — "Route" could collide with Gateway API or other CRDs)
	var knativeRouteGVR schema.GroupVersionResource
	hasKnativeRoutes := false
	if resourceDiscovery != nil {
		knativeRouteGVR, hasKnativeRoutes = resourceDiscovery.GetGVRWithGroup("Route", "serving.knative.dev")
	}
	knativeRouteIDs := make(map[string]string)             // ns/name -> krouteID
	var knativeRouteResources []*unstructured.Unstructured // Store for second pass
	if hasKnativeRoutes && dynamicCache != nil {
		knativeRoutes, krouteErr := dynamicCache.ListNamespaces(knativeRouteGVR, opts.Namespaces)
		if krouteErr != nil {
			log.Printf("WARNING [topology] Failed to list KNative Routes: %v", krouteErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list KNative Routes: %v", krouteErr))
		}
		for _, kroute := range knativeRoutes {
			ns := kroute.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := kroute.GetName()

			krouteID := fmt.Sprintf("knativeroute/%s/%s", ns, name)
			knativeRouteIDs[ns+"/"+name] = krouteID

			nodes = append(nodes, Node{
				ID:     krouteID,
				Kind:   KindKnativeRoute,
				Name:   name,
				Status: extractGenericStatus(kroute),
				Data: map[string]any{
					"namespace":  ns,
					"labels":     kroute.GetLabels(),
					"apiVersion": kroute.GetAPIVersion(),
				},
			})

			knativeRouteResources = append(knativeRouteResources, kroute)
		}
	}

	// 1o. Add KNative Eventing nodes (CRD - fetched via dynamic cache)
	// Broker, Trigger, PingSource, ApiServerSource, ContainerSource, SinkBinding

	// KNative Broker (needs group-qualified lookup — "Broker" could collide with messaging CRDs)
	var knativeBrokerGVR schema.GroupVersionResource
	hasKnativeBrokers := false
	if resourceDiscovery != nil {
		knativeBrokerGVR, hasKnativeBrokers = resourceDiscovery.GetGVRWithGroup("Broker", "eventing.knative.dev")
	}
	knativeBrokerIDs := make(map[string]string) // ns/name -> brokerID
	if hasKnativeBrokers && dynamicCache != nil {
		knativeBrokers, brokerErr := dynamicCache.ListNamespaces(knativeBrokerGVR, opts.Namespaces)
		if brokerErr != nil {
			log.Printf("WARNING [topology] Failed to list KNative Brokers: %v", brokerErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list KNative Brokers: %v", brokerErr))
		}
		for _, broker := range knativeBrokers {
			ns := broker.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := broker.GetName()

			brokerID := fmt.Sprintf("broker/%s/%s", ns, name)
			knativeBrokerIDs[ns+"/"+name] = brokerID

			nodes = append(nodes, Node{
				ID:     brokerID,
				Kind:   KindBroker,
				Name:   name,
				Status: extractGenericStatus(broker),
				Data: map[string]any{
					"namespace":  ns,
					"labels":     broker.GetLabels(),
					"apiVersion": broker.GetAPIVersion(),
				},
			})
		}
	}

	// KNative Trigger (needs group-qualified lookup — "Trigger" could collide with KEDA or other CRDs)
	var knativeTriggerGVR schema.GroupVersionResource
	hasKnativeTriggers := false
	if resourceDiscovery != nil {
		knativeTriggerGVR, hasKnativeTriggers = resourceDiscovery.GetGVRWithGroup("Trigger", "eventing.knative.dev")
	}
	knativeTriggerIDs := make(map[string]string)             // ns/name -> triggerID
	var knativeTriggerResources []*unstructured.Unstructured // Store for second pass
	if hasKnativeTriggers && dynamicCache != nil {
		knativeTriggers, triggerErr := dynamicCache.ListNamespaces(knativeTriggerGVR, opts.Namespaces)
		if triggerErr != nil {
			log.Printf("WARNING [topology] Failed to list KNative Triggers: %v", triggerErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list KNative Triggers: %v", triggerErr))
		}
		for _, trigger := range knativeTriggers {
			ns := trigger.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := trigger.GetName()

			triggerID := fmt.Sprintf("trigger/%s/%s", ns, name)
			knativeTriggerIDs[ns+"/"+name] = triggerID

			nodes = append(nodes, Node{
				ID:     triggerID,
				Kind:   KindTrigger,
				Name:   name,
				Status: extractGenericStatus(trigger),
				Data: map[string]any{
					"namespace":  ns,
					"labels":     trigger.GetLabels(),
					"apiVersion": trigger.GetAPIVersion(),
				},
			})

			knativeTriggerResources = append(knativeTriggerResources, trigger)
		}
	}

	// KNative event sources: PingSource, ApiServerSource, ContainerSource, SinkBinding
	type knativeSourceDef struct {
		kind     string
		nodeKind NodeKind
		prefix   string
	}
	knativeSourceDefs := []knativeSourceDef{
		{"PingSource", KindPingSource, "pingsource"},
		{"ApiServerSource", KindApiServerSource, "apiserversource"},
		{"ContainerSource", KindContainerSource, "containersource"},
		{"SinkBinding", KindSinkBinding, "sinkbinding"},
	}
	var knativeSourceResources []*unstructured.Unstructured // Store all sources for second pass edge creation
	knativeSourceKinds := make(map[*unstructured.Unstructured]knativeSourceDef)
	for _, srcDef := range knativeSourceDefs {
		var srcGVR schema.GroupVersionResource
		hasSrc := false
		if resourceDiscovery != nil {
			srcGVR, hasSrc = resourceDiscovery.GetGVR(srcDef.kind)
		}
		if !hasSrc || dynamicCache == nil {
			continue
		}
		sources, srcErr := dynamicCache.ListNamespaces(srcGVR, opts.Namespaces)
		if srcErr != nil {
			log.Printf("WARNING [topology] Failed to list KNative %s: %v", srcDef.kind, srcErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list KNative %s: %v", srcDef.kind, srcErr))
			continue
		}
		for _, src := range sources {
			ns := src.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := src.GetName()

			srcID := fmt.Sprintf("%s/%s/%s", srcDef.prefix, ns, name)

			nodes = append(nodes, Node{
				ID:     srcID,
				Kind:   srcDef.nodeKind,
				Name:   name,
				Status: extractGenericStatus(src),
				Data: map[string]any{
					"namespace":  ns,
					"labels":     src.GetLabels(),
					"apiVersion": src.GetAPIVersion(),
				},
			})

			knativeSourceResources = append(knativeSourceResources, src)
			knativeSourceKinds[src] = srcDef
		}
	}

	// KNative Channel (needs group-qualified lookup — "Channel" could collide with other CRDs)
	var knativeChannelGVR schema.GroupVersionResource
	hasKnativeChannels := false
	if resourceDiscovery != nil {
		knativeChannelGVR, hasKnativeChannels = resourceDiscovery.GetGVRWithGroup("Channel", "messaging.knative.dev")
	}
	knativeChannelIDs := make(map[string]string) // ns/name -> channelID
	if hasKnativeChannels && dynamicCache != nil {
		knativeChannels, chanErr := dynamicCache.ListNamespaces(knativeChannelGVR, opts.Namespaces)
		if chanErr != nil {
			log.Printf("WARNING [topology] Failed to list KNative Channels: %v", chanErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list KNative Channels: %v", chanErr))
		}
		for _, ch := range knativeChannels {
			ns := ch.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := ch.GetName()

			chID := fmt.Sprintf("channel/%s/%s", ns, name)
			knativeChannelIDs[ns+"/"+name] = chID

			nodes = append(nodes, Node{
				ID:     chID,
				Kind:   KindChannel,
				Name:   name,
				Status: extractGenericStatus(ch),
				Data: map[string]any{
					"namespace":  ns,
					"labels":     ch.GetLabels(),
					"apiVersion": ch.GetAPIVersion(),
				},
			})
		}
	}

	// 1p. Add Traefik nodes (CRD - fetched via dynamic cache)
	// IngressRoute, IngressRouteTCP, IngressRouteUDP, Middleware, MiddlewareTCP,
	// TraefikService, ServersTransport, ServersTransportTCP, TLSOption, TLSStore
	// Edges are created in a second pass after service IDs are populated.

	type traefikRouteDef struct {
		kind     string   // CRD kind for GetGVR
		nodeKind NodeKind // topology NodeKind
		prefix   string   // node ID prefix
	}
	traefikRouteDefs := []traefikRouteDef{
		{"IngressRoute", KindIngressRoute, "ingressroute"},
		{"IngressRouteTCP", KindIngressRouteTCP, "ingressroutetcp"},
		{"IngressRouteUDP", KindIngressRouteUDP, "ingressrouteudp"},
	}

	// Maps for edge creation in second pass
	traefikRouteIDs := make(map[string]string)                                // prefix:ns/name -> routeID
	var traefikRouteResources []*unstructured.Unstructured                    // Store for edge creation
	traefikRouteKinds := make(map[*unstructured.Unstructured]traefikRouteDef) // resource -> def

	for _, def := range traefikRouteDefs {
		var gvr schema.GroupVersionResource
		hasKind := false
		if resourceDiscovery != nil {
			gvr, hasKind = resourceDiscovery.GetGVR(def.kind)
		}
		if !hasKind || dynamicCache == nil {
			continue
		}
		resources, listErr := dynamicCache.ListNamespaces(gvr, opts.Namespaces)
		if listErr != nil {
			log.Printf("WARNING [topology] Failed to list Traefik %s: %v", def.kind, listErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list Traefik %s: %v", def.kind, listErr))
			continue
		}
		for _, res := range resources {
			ns := res.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := res.GetName()

			resID := fmt.Sprintf("%s/%s/%s", def.prefix, ns, name)
			traefikRouteIDs[def.prefix+":"+ns+"/"+name] = resID

			routes, _, _ := unstructured.NestedSlice(res.Object, "spec", "routes")
			entryPoints, _, _ := unstructured.NestedStringSlice(res.Object, "spec", "entryPoints")

			// Count total services across all routes
			svcCount := 0
			for _, r := range routes {
				if rm, ok := r.(map[string]any); ok {
					svcs, _, _ := unstructured.NestedSlice(rm, "services")
					svcCount += len(svcs)
				}
			}

			nodeStatus := StatusHealthy
			if len(routes) == 0 || svcCount == 0 {
				nodeStatus = StatusUnhealthy
			}

			nodes = append(nodes, Node{
				ID:     resID,
				Kind:   def.nodeKind,
				Name:   name,
				Status: nodeStatus,
				Data: map[string]any{
					"namespace":   ns,
					"entryPoints": entryPoints,
					"routeCount":  len(routes),
					"labels":      res.GetLabels(),
					"apiVersion":  res.GetAPIVersion(),
				},
			})

			traefikRouteResources = append(traefikRouteResources, res)
			traefikRouteKinds[res] = def
		}
	}

	// Traefik Middleware and MiddlewareTCP
	type traefikSimpleDef struct {
		kind     string
		nodeKind NodeKind
		prefix   string
	}
	traefikMiddlewareDefs := []traefikSimpleDef{
		{"Middleware", KindMiddleware, "middleware"},
		{"MiddlewareTCP", KindMiddlewareTCP, "middlewaretcp"},
	}
	middlewareIDs := make(map[string]string)             // ns/name -> mwID
	var middlewareResources []*unstructured.Unstructured // Store for chain edge creation
	for _, def := range traefikMiddlewareDefs {
		var gvr schema.GroupVersionResource
		hasKind := false
		if resourceDiscovery != nil {
			gvr, hasKind = resourceDiscovery.GetGVR(def.kind)
		}
		if !hasKind || dynamicCache == nil {
			continue
		}
		resources, listErr := dynamicCache.ListNamespaces(gvr, opts.Namespaces)
		if listErr != nil {
			log.Printf("WARNING [topology] Failed to list Traefik %s: %v", def.kind, listErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list Traefik %s: %v", def.kind, listErr))
			continue
		}
		for _, res := range resources {
			ns := res.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := res.GetName()

			mwID := fmt.Sprintf("%s/%s/%s", def.prefix, ns, name)
			middlewareIDs[def.prefix+":"+ns+"/"+name] = mwID

			nodes = append(nodes, Node{
				ID:     mwID,
				Kind:   def.nodeKind,
				Name:   name,
				Status: extractGenericStatus(res),
				Data: map[string]any{
					"namespace":  ns,
					"labels":     res.GetLabels(),
					"apiVersion": res.GetAPIVersion(),
				},
			})

			if def.kind == "Middleware" {
				middlewareResources = append(middlewareResources, res)
			}
		}
	}

	// Traefik TraefikService
	var traefikServiceGVR schema.GroupVersionResource
	hasTraefikServices := false
	if resourceDiscovery != nil {
		traefikServiceGVR, hasTraefikServices = resourceDiscovery.GetGVR("TraefikService")
	}
	traefikServiceIDs := make(map[string]string)             // ns/name -> tsID
	var traefikServiceResources []*unstructured.Unstructured // Store for edge creation
	if hasTraefikServices && dynamicCache != nil {
		tsvcs, tsErr := dynamicCache.ListNamespaces(traefikServiceGVR, opts.Namespaces)
		if tsErr != nil {
			log.Printf("WARNING [topology] Failed to list Traefik TraefikServices: %v", tsErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list Traefik TraefikServices: %v", tsErr))
		}
		for _, ts := range tsvcs {
			ns := ts.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := ts.GetName()

			tsID := fmt.Sprintf("traefikservice/%s/%s", ns, name)
			traefikServiceIDs[ns+"/"+name] = tsID

			// Determine type: weighted, mirroring, or highestRandomWeight
			spec, _, _ := unstructured.NestedMap(ts.Object, "spec")
			tsType := "unknown"
			svcCount := 0
			if weighted, ok := spec["weighted"]; ok {
				tsType = "weighted"
				if wm, ok := weighted.(map[string]any); ok {
					if svcs, _, _ := unstructured.NestedSlice(wm, "services"); svcs != nil {
						svcCount = len(svcs)
					}
				}
			} else if mirroring, ok := spec["mirroring"]; ok {
				tsType = "mirroring"
				svcCount = 1 // primary
				if mm, ok := mirroring.(map[string]any); ok {
					if mirrors, _, _ := unstructured.NestedSlice(mm, "mirrors"); mirrors != nil {
						svcCount += len(mirrors)
					}
				}
			} else if hrw, ok := spec["highestRandomWeight"]; ok {
				tsType = "highestRandomWeight"
				if hm, ok := hrw.(map[string]any); ok {
					if svcs, _, _ := unstructured.NestedSlice(hm, "services"); svcs != nil {
						svcCount = len(svcs)
					}
				}
			}

			nodeStatus := StatusHealthy
			if svcCount == 0 {
				nodeStatus = StatusUnhealthy
			}

			nodes = append(nodes, Node{
				ID:     tsID,
				Kind:   KindTraefikService,
				Name:   name,
				Status: nodeStatus,
				Data: map[string]any{
					"namespace":    ns,
					"type":         tsType,
					"serviceCount": svcCount,
					"labels":       ts.GetLabels(),
					"apiVersion":   ts.GetAPIVersion(),
				},
			})

			traefikServiceResources = append(traefikServiceResources, ts)
		}
	}

	// Traefik config-only kinds: ServersTransport, ServersTransportTCP, TLSOption, TLSStore
	traefikConfigDefs := []traefikSimpleDef{
		{"ServersTransport", KindServersTransport, "serverstransport"},
		{"ServersTransportTCP", KindServersTransportTCP, "serverstransporttcp"},
		{"TLSOption", KindTLSOption, "tlsoption"},
		{"TLSStore", KindTLSStore, "tlsstore"},
	}
	traefikConfigIDs := make(map[string]string) // prefix:ns/name -> configID
	type traefikConfigEntry struct {
		resource unstructured.Unstructured
		prefix   string
	}
	var serversTransportResources []traefikConfigEntry
	var tlsOptionResources []traefikConfigEntry
	var tlsStoreResources []traefikConfigEntry
	for _, def := range traefikConfigDefs {
		var gvr schema.GroupVersionResource
		hasKind := false
		if resourceDiscovery != nil {
			gvr, hasKind = resourceDiscovery.GetGVR(def.kind)
		}
		if !hasKind || dynamicCache == nil {
			continue
		}
		resources, listErr := dynamicCache.ListNamespaces(gvr, opts.Namespaces)
		if listErr != nil {
			log.Printf("WARNING [topology] Failed to list Traefik %s: %v", def.kind, listErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list Traefik %s: %v", def.kind, listErr))
			continue
		}
		for _, res := range resources {
			ns := res.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := res.GetName()

			cfgID := fmt.Sprintf("%s/%s/%s", def.prefix, ns, name)
			traefikConfigIDs[def.prefix+":"+ns+"/"+name] = cfgID

			nodes = append(nodes, Node{
				ID:     cfgID,
				Kind:   def.nodeKind,
				Name:   name,
				Status: StatusHealthy,
				Data: map[string]any{
					"namespace":  ns,
					"labels":     res.GetLabels(),
					"apiVersion": res.GetAPIVersion(),
				},
			})

			switch def.prefix {
			case "serverstransport", "serverstransporttcp":
				serversTransportResources = append(serversTransportResources, traefikConfigEntry{resource: *res, prefix: def.prefix})
			case "tlsoption":
				tlsOptionResources = append(tlsOptionResources, traefikConfigEntry{resource: *res, prefix: def.prefix})
			case "tlsstore":
				tlsStoreResources = append(tlsStoreResources, traefikConfigEntry{resource: *res, prefix: def.prefix})
			}
		}
	}

	// 1q. Add Contour HTTPProxy nodes (CRD - fetched via dynamic cache)
	// Edges are created in a second pass after service IDs are populated.
	httpProxyIDs := make(map[string]string)             // ns/name → nodeID
	var httpProxyResources []*unstructured.Unstructured // Store for edge creation
	{
		var httpProxyGVR schema.GroupVersionResource
		hasHTTPProxy := false
		if resourceDiscovery != nil {
			httpProxyGVR, hasHTTPProxy = resourceDiscovery.GetGVR("HTTPProxy")
		}
		if hasHTTPProxy && dynamicCache != nil {
			resources, listErr := dynamicCache.ListNamespaces(httpProxyGVR, opts.Namespaces)
			if listErr != nil {
				log.Printf("WARNING [topology] Failed to list Contour HTTPProxy: %v", listErr)
				warnings = append(warnings, fmt.Sprintf("Failed to list Contour HTTPProxy: %v", listErr))
			}
			for _, res := range resources {
				ns := res.GetNamespace()
				if !opts.MatchesNamespaceFilter(ns) {
					continue
				}
				name := res.GetName()

				resID := fmt.Sprintf("httpproxy/%s/%s", ns, name)
				httpProxyIDs[ns+"/"+name] = resID

				// Extract spec fields
				fqdn, _, _ := unstructured.NestedString(res.Object, "spec", "virtualhost", "fqdn")
				routes, _, _ := unstructured.NestedSlice(res.Object, "spec", "routes")
				includes, _, _ := unstructured.NestedSlice(res.Object, "spec", "includes")
				_, hasTLS, _ := unstructured.NestedMap(res.Object, "spec", "virtualhost", "tls")

				// Status logic using status.currentStatus
				nodeStatus := extractGenericStatus(res)
				currentStatus, _, _ := unstructured.NestedString(res.Object, "status", "currentStatus")
				switch strings.ToLower(currentStatus) {
				case "valid":
					nodeStatus = StatusHealthy
				case "invalid":
					nodeStatus = StatusUnhealthy
				case "orphaned":
					nodeStatus = StatusDegraded
				}

				nodes = append(nodes, Node{
					ID:     resID,
					Kind:   KindHTTPProxy,
					Name:   name,
					Status: nodeStatus,
					Data: map[string]any{
						"namespace":    ns,
						"fqdn":         fqdn,
						"routeCount":   len(routes),
						"includeCount": len(includes),
						"hasTLS":       hasTLS,
						"labels":       res.GetLabels(),
						"apiVersion":   res.GetAPIVersion(),
					},
				})

				httpProxyResources = append(httpProxyResources, res)
			}
		}
	}

	// 2. Add DaemonSet nodes
	var daemonsets []*appsv1.DaemonSet
	{
		dss, dssErr := b.provider.DaemonSets()
		if dssErr != nil {
			log.Printf("WARNING [topology] Failed to list DaemonSets: %v", dssErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list DaemonSets: %v", dssErr))
		}
		daemonsets = dss
	}
	for _, ds := range daemonsets {
		if !opts.MatchesNamespaceFilter(ds.Namespace) {
			continue
		}

		dsID := fmt.Sprintf("daemonset/%s/%s", ds.Namespace, ds.Name)

		ready := ds.Status.NumberReady
		total := ds.Status.DesiredNumberScheduled

		// Get status summary from cache for detailed issue reporting
		statusSummary := ""
		statusIssue := ""
		if resourceStatus := b.provider.GetResourceStatus("DaemonSet", ds.Namespace, ds.Name); resourceStatus != nil {
			statusSummary = resourceStatus.Summary
			statusIssue = resourceStatus.Issue
		}

		nodes = append(nodes, Node{
			ID:     dsID,
			Kind:   KindDaemonSet,
			Name:   ds.Name,
			Status: healthLevelToStatus(health.Workload(ds, time.Now()).Level),
			Data: map[string]any{
				"namespace":     ds.Namespace,
				"readyReplicas": ready,
				"totalReplicas": total,
				"labels":        ds.Labels,
				"statusSummary": statusSummary,
				"statusIssue":   statusIssue,
			},
		})

		refs := extractWorkloadReferences(ds.Spec.Template.Spec)
		trackWorkloadRefs(dsID, ds.Namespace, refs)
	}

	// 3. Add StatefulSet nodes
	var statefulsets []*appsv1.StatefulSet
	{
		stss, stssErr := b.provider.StatefulSets()
		if stssErr != nil {
			log.Printf("WARNING [topology] Failed to list StatefulSets: %v", stssErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list StatefulSets: %v", stssErr))
		}
		statefulsets = stss
	}
	for _, sts := range statefulsets {
		if !opts.MatchesNamespaceFilter(sts.Namespace) {
			continue
		}

		stsID := fmt.Sprintf("statefulset/%s/%s", sts.Namespace, sts.Name)
		statefulSetIDs[sts.Namespace+"/"+sts.Name] = stsID

		ready := sts.Status.ReadyReplicas
		total := int32(1) // K8s defaults to 1 when unset
		if sts.Spec.Replicas != nil {
			total = *sts.Spec.Replicas
		}

		// Get status summary from cache for detailed issue reporting
		statusSummary := ""
		statusIssue := ""
		if resourceStatus := b.provider.GetResourceStatus("StatefulSet", sts.Namespace, sts.Name); resourceStatus != nil {
			statusSummary = resourceStatus.Summary
			statusIssue = resourceStatus.Issue
		}

		nodes = append(nodes, Node{
			ID:     stsID,
			Kind:   KindStatefulSet,
			Name:   sts.Name,
			Status: healthLevelToStatus(health.Workload(sts, time.Now()).Level),
			Data: map[string]any{
				"namespace":     sts.Namespace,
				"readyReplicas": ready,
				"totalReplicas": total,
				"labels":        sts.Labels,
				"statusSummary": statusSummary,
				"statusIssue":   statusIssue,
			},
		})

		refs := extractWorkloadReferences(sts.Spec.Template.Spec)
		trackWorkloadRefs(stsID, sts.Namespace, refs)
	}

	// 4. Add CronJob nodes
	var cronjobs []*batchv1.CronJob
	{
		cjs, cjsErr := b.provider.CronJobs()
		if cjsErr != nil {
			log.Printf("WARNING [topology] Failed to list CronJobs: %v", cjsErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list CronJobs: %v", cjsErr))
		}
		cronjobs = cjs
	}
	for _, cj := range cronjobs {
		if !opts.MatchesNamespaceFilter(cj.Namespace) {
			continue
		}

		cjID := fmt.Sprintf("cronjob/%s/%s", cj.Namespace, cj.Name)
		cronJobIDs[cj.Namespace+"/"+cj.Name] = cjID

		nodes = append(nodes, Node{
			ID:     cjID,
			Kind:   KindCronJob,
			Name:   cj.Name,
			Status: healthLevelToStatus(health.Workload(cj, time.Now()).Level),
			Data: map[string]any{
				"namespace":        cj.Namespace,
				"schedule":         cj.Spec.Schedule,
				"suspend":          cj.Spec.Suspend != nil && *cj.Spec.Suspend,
				"activeJobs":       len(cj.Status.Active),
				"lastScheduleTime": cj.Status.LastScheduleTime,
				"labels":           cj.Labels,
			},
		})

		// Track ConfigMap/Secret/PVC references
		refs := extractWorkloadReferences(cj.Spec.JobTemplate.Spec.Template.Spec)
		trackWorkloadRefs(cjID, cj.Namespace, refs)
	}

	// 4b. Add Argo Workflow/CronWorkflow nodes
	var cronWorkflowGVR schema.GroupVersionResource
	hasCronWorkflows := false
	var workflowGVR schema.GroupVersionResource
	hasWorkflows := false
	var workflowTemplateGVR schema.GroupVersionResource
	hasWorkflowTemplates := false
	var clusterWorkflowTemplateGVR schema.GroupVersionResource
	hasClusterWorkflowTemplates := false
	if resourceDiscovery != nil {
		cronWorkflowGVR, hasCronWorkflows = resourceDiscovery.GetGVRWithGroup("CronWorkflow", "argoproj.io")
		workflowGVR, hasWorkflows = resourceDiscovery.GetGVRWithGroup("Workflow", "argoproj.io")
		workflowTemplateGVR, hasWorkflowTemplates = resourceDiscovery.GetGVRWithGroup("WorkflowTemplate", "argoproj.io")
		clusterWorkflowTemplateGVR, hasClusterWorkflowTemplates = resourceDiscovery.GetGVRWithGroup("ClusterWorkflowTemplate", "argoproj.io")
	}
	if hasWorkflowTemplates && dynamicCache != nil {
		workflowTemplates, err := dynamicCache.ListNamespaces(workflowTemplateGVR, opts.Namespaces)
		if err != nil {
			log.Printf("WARNING [topology] Failed to list Argo WorkflowTemplates: %v", err)
			warnings = append(warnings, fmt.Sprintf("Failed to list Argo WorkflowTemplates: %v", err))
		}
		for _, wt := range workflowTemplates {
			ns := wt.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := wt.GetName()
			entrypoint, _, _ := unstructured.NestedString(wt.Object, "spec", "entrypoint")
			templates, _, _ := unstructured.NestedSlice(wt.Object, "spec", "templates")
			wtID := fmt.Sprintf("workflowtemplate/%s/%s", ns, name)
			workflowTemplateIDs[ns+"/"+name] = wtID
			workflowTemplateNodes[wtID] = Node{
				ID:     wtID,
				Kind:   KindWorkflowTemplate,
				Name:   name,
				Status: StatusNeutral,
				Data: map[string]any{
					"namespace":     ns,
					"entrypoint":    entrypoint,
					"templateCount": len(templates),
					"labels":        wt.GetLabels(),
					"apiVersion":    wt.GetAPIVersion(),
				},
			}
		}
	}
	if hasClusterWorkflowTemplates && dynamicCache != nil {
		clusterWorkflowTemplates, err := dynamicCache.ListNamespaces(clusterWorkflowTemplateGVR, opts.Namespaces)
		if err != nil {
			log.Printf("WARNING [topology] Failed to list Argo ClusterWorkflowTemplates: %v", err)
			warnings = append(warnings, fmt.Sprintf("Failed to list Argo ClusterWorkflowTemplates: %v", err))
		}
		for _, cwt := range clusterWorkflowTemplates {
			name := cwt.GetName()
			entrypoint, _, _ := unstructured.NestedString(cwt.Object, "spec", "entrypoint")
			templates, _, _ := unstructured.NestedSlice(cwt.Object, "spec", "templates")
			cwtID := fmt.Sprintf("clusterworkflowtemplate//%s", name)
			clusterWorkflowTemplateIDs[name] = cwtID
			workflowTemplateNodes[cwtID] = Node{
				ID:     cwtID,
				Kind:   KindClusterWorkflowTemplate,
				Name:   name,
				Status: StatusNeutral,
				Data: map[string]any{
					"entrypoint":    entrypoint,
					"templateCount": len(templates),
					"labels":        cwt.GetLabels(),
					"apiVersion":    cwt.GetAPIVersion(),
				},
			}
		}
	}
	if hasCronWorkflows && dynamicCache != nil {
		cronWorkflows, err := dynamicCache.ListNamespaces(cronWorkflowGVR, opts.Namespaces)
		if err != nil {
			log.Printf("WARNING [topology] Failed to list Argo CronWorkflows: %v", err)
			warnings = append(warnings, fmt.Sprintf("Failed to list Argo CronWorkflows: %v", err))
		}
		for _, cwf := range cronWorkflows {
			ns := cwf.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := cwf.GetName()
			cwfID := fmt.Sprintf("cronworkflow/%s/%s", ns, name)
			cronWorkflowIDs[ns+"/"+name] = cwfID
			suspended, _, _ := unstructured.NestedBool(cwf.Object, "spec", "suspend")
			lastScheduled, _, _ := unstructured.NestedString(cwf.Object, "status", "lastScheduledTime")
			nodes = append(nodes, Node{
				ID:     cwfID,
				Kind:   KindCronWorkflow,
				Name:   name,
				Status: cronWorkflowTopologyStatus(cwf),
				Data: map[string]any{
					"namespace":         ns,
					"schedule":          cronWorkflowScheduleString(cwf),
					"suspend":           suspended,
					"lastScheduledTime": lastScheduled,
					"labels":            cwf.GetLabels(),
					"apiVersion":        cwf.GetAPIVersion(),
				},
			})
			edges = addArgoWorkflowTemplateEdges(edges, cwfID, ns, argoWorkflowTemplateRefsFromWorkflowSpec(cwf.Object, "spec", "workflowSpec"), workflowTemplateIDs, clusterWorkflowTemplateIDs)
		}
	}
	if hasWorkflows && dynamicCache != nil {
		workflows, err := dynamicCache.ListNamespaces(workflowGVR, opts.Namespaces)
		if err != nil {
			log.Printf("WARNING [topology] Failed to list Argo Workflows: %v", err)
			warnings = append(warnings, fmt.Sprintf("Failed to list Argo Workflows: %v", err))
		}
		for _, wf := range workflows {
			ns := wf.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := wf.GetName()
			wfID := fmt.Sprintf("workflow/%s/%s", ns, name)
			workflowIDs[ns+"/"+name] = wfID
			phase, _, _ := unstructured.NestedString(wf.Object, "status", "phase")
			progress, _, _ := unstructured.NestedString(wf.Object, "status", "progress")
			startedAt, _, _ := unstructured.NestedString(wf.Object, "status", "startedAt")
			finishedAt, _, _ := unstructured.NestedString(wf.Object, "status", "finishedAt")
			template, _, _ := unstructured.NestedString(wf.Object, "spec", "workflowTemplateRef", "name")
			nodes = append(nodes, Node{
				ID:     wfID,
				Kind:   KindWorkflow,
				Name:   name,
				Status: workflowTopologyStatus(phase),
				Data: map[string]any{
					"namespace":  ns,
					"phase":      phase,
					"progress":   progress,
					"startedAt":  startedAt,
					"finishedAt": finishedAt,
					"template":   template,
					"labels":     wf.GetLabels(),
					"apiVersion": wf.GetAPIVersion(),
				},
			})
			edges = addArgoWorkflowTemplateEdges(edges, wfID, ns, argoWorkflowTemplateRefsFromWorkflowSpec(wf.Object, "spec"), workflowTemplateIDs, clusterWorkflowTemplateIDs)
			if owner := argoWorkflowCronOwnerName(wf); owner != "" {
				if cwfID, ok := cronWorkflowIDs[ns+"/"+owner]; ok {
					edges = append(edges, Edge{
						ID:     fmt.Sprintf("%s-to-%s", cwfID, wfID),
						Source: cwfID,
						Target: wfID,
						Type:   EdgeManages,
					})
					workflowToCronWorkflow[ns+"/"+name] = cwfID
				}
			}
		}
	}
	if len(workflowTemplateNodes) > 0 {
		templateIDs := make([]string, 0, len(workflowTemplateNodes))
		for id := range workflowTemplateNodes {
			templateIDs = append(templateIDs, id)
		}
		sort.Strings(templateIDs)
		for _, id := range templateIDs {
			if node, ok := workflowTemplateNodes[id]; ok {
				nodes = append(nodes, node)
			}
		}
	}

	// 5. Add Job nodes
	var jobs []*batchv1.Job
	{
		js, jsErr := b.provider.Jobs()
		if jsErr != nil {
			log.Printf("WARNING [topology] Failed to list Jobs: %v", jsErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list Jobs: %v", jsErr))
		}
		jobs = js
	}
	for _, job := range jobs {
		if !opts.MatchesNamespaceFilter(job.Namespace) {
			continue
		}

		jobID := fmt.Sprintf("job/%s/%s", job.Namespace, job.Name)
		jobIDs[job.Namespace+"/"+job.Name] = jobID

		// Determine status
		status := getJobStatus(job)
		startTime := ""
		if job.Status.StartTime != nil {
			startTime = job.Status.StartTime.Format(time.RFC3339)
		}
		completionTime := ""
		if job.Status.CompletionTime != nil {
			completionTime = job.Status.CompletionTime.Format(time.RFC3339)
		}

		nodes = append(nodes, Node{
			ID:     jobID,
			Kind:   KindJob,
			Name:   job.Name,
			Status: status,
			Data: map[string]any{
				"namespace":      job.Namespace,
				"completions":    job.Spec.Completions,
				"parallelism":    job.Spec.Parallelism,
				"succeeded":      job.Status.Succeeded,
				"failed":         job.Status.Failed,
				"active":         job.Status.Active,
				"startTime":      startTime,
				"completionTime": completionTime,
				"labels":         job.Labels,
			},
		})

		// Track ConfigMap/Secret/PVC references
		refs := extractWorkloadReferences(job.Spec.Template.Spec)
		trackWorkloadRefs(jobID, job.Namespace, refs)

		// Connect to owner CronJob or KEDA ScaledJob
		for _, ownerRef := range job.OwnerReferences {
			switch ownerRef.Kind {
			case "CronJob":
				ownerKey := job.Namespace + "/" + ownerRef.Name
				if ownerID, ok := cronJobIDs[ownerKey]; ok {
					edges = append(edges, Edge{
						ID:     fmt.Sprintf("%s-to-%s", ownerID, jobID),
						Source: ownerID,
						Target: jobID,
						Type:   EdgeManages,
					})
					// Track for shortcut edges (CronJob -> Pod)
					jobKey := job.Namespace + "/" + job.Name
					jobToCronJob[jobKey] = ownerID
				}
			case "ScaledJob":
				ownerKey := job.Namespace + "/" + ownerRef.Name
				if ownerID, ok := scaledJobIDs[ownerKey]; ok {
					edges = append(edges, Edge{
						ID:     fmt.Sprintf("%s-to-%s", ownerID, jobID),
						Source: ownerID,
						Target: jobID,
						Type:   EdgeManages,
					})
					jobKey := job.Namespace + "/" + job.Name
					jobToScaledJob[jobKey] = ownerID
				}
			}
		}
	}

	// 6. Add ReplicaSet nodes (active ones) - if enabled
	// Even if not shown, we still track them for shortcut edges
	var replicasets []*appsv1.ReplicaSet
	{
		rss, rssErr := b.provider.ReplicaSets()
		if rssErr != nil {
			log.Printf("WARNING [topology] Failed to list ReplicaSets: %v", rssErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list ReplicaSets: %v", rssErr))
		}
		replicasets = rss
	}
	for _, rs := range replicasets {
		if !opts.MatchesNamespaceFilter(rs.Namespace) {
			continue
		}

		// Skip inactive ReplicaSets (old rollouts)
		if rs.Spec.Replicas != nil && *rs.Spec.Replicas == 0 {
			continue
		}

		rsID := fmt.Sprintf("replicaset/%s/%s", rs.Namespace, rs.Name)
		replicaSetIDs[rs.Namespace+"/"+rs.Name] = rsID

		// Track owner for shortcut edges regardless of visibility
		for _, ownerRef := range rs.OwnerReferences {
			ownerKey := rs.Namespace + "/" + ownerRef.Name
			rsKey := rs.Namespace + "/" + rs.Name
			if ownerRef.Kind == "Deployment" {
				if ownerID, ok := deploymentIDs[ownerKey]; ok {
					replicaSetToDeployment[rsKey] = ownerID
				}
			} else if ownerRef.Kind == "Rollout" {
				if ownerID, ok := rolloutIDs[ownerKey]; ok {
					replicaSetToRollout[rsKey] = ownerID
				}
			}
		}

		// Only add node and edges if ReplicaSets are enabled
		if opts.IncludeReplicaSets {
			ready := rs.Status.ReadyReplicas
			total := int32(1) // K8s defaults to 1 when unset
			if rs.Spec.Replicas != nil {
				total = *rs.Spec.Replicas
			}

			nodes = append(nodes, Node{
				ID:     rsID,
				Kind:   KindReplicaSet,
				Name:   rs.Name,
				Status: healthLevelToStatus(health.Workload(rs, time.Now()).Level),
				Data: map[string]any{
					"namespace":     rs.Namespace,
					"readyReplicas": ready,
					"totalReplicas": total,
					"labels":        rs.Labels,
				},
			})

			// Connect to owner Deployment or Rollout
			for _, ownerRef := range rs.OwnerReferences {
				ownerKey := rs.Namespace + "/" + ownerRef.Name
				var ownerID string
				var found bool
				if ownerRef.Kind == "Deployment" {
					ownerID, found = deploymentIDs[ownerKey]
				} else if ownerRef.Kind == "Rollout" {
					ownerID, found = rolloutIDs[ownerKey]
				}
				if found {
					edges = append(edges, Edge{
						ID:     fmt.Sprintf("%s-to-%s", ownerID, rsID),
						Source: ownerID,
						Target: rsID,
						Type:   EdgeManages,
					})
				}
			}
		}
	}

	// 5. Add Pod nodes - grouped by app label when there are multiple pods
	var pods []*corev1.Pod
	{
		ps, psErr := b.provider.Pods()
		if psErr != nil {
			log.Printf("WARNING [topology] Failed to list Pods: %v", psErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list Pods: %v", psErr))
		}
		pods = ps
	}
	// podSummaries accumulates per-workload pod health in summary mode; stamped
	// onto the workload nodes just before return. Empty (and the stamp a no-op)
	// in normal mode.
	podSummaries := make(map[string]*PodSummary)
	if len(pods) > 0 && opts.SummaryMode {
		// Summary mode: collapse the pod tier entirely. Roll each pod's health
		// onto its owning workload node. Pods with no resolvable workload
		// (standalone, bare ReplicaSet, or a controller whose node wasn't
		// created) are aggregated into ONE summary-only node per namespace —
		// counts only, no per-pod array and no expand affordance — so a large
		// orphan set can't re-introduce the pod-tier payload/render cost.
		existingNodeIDs := make(map[string]bool, len(nodes))
		for _, n := range nodes {
			existingNodeIDs[n.ID] = true
		}
		orphanByNS := make(map[string]*PodSummary)
		orphanRestarts := make(map[string]int32)
		for _, pod := range pods {
			if !opts.MatchesNamespaceFilter(pod.Namespace) {
				continue
			}
			workloadID := b.resolvePodWorkloadID(pod, existingNodeIDs, replicaSetToDeployment, replicaSetToRollout, jobIDs, workflowIDs)
			if workloadID == "" {
				addPodHealth(orphanByNS, pod.Namespace, pod)
				orphanRestarts[pod.Namespace] += ComputePodRestarts(pod)
				continue
			}
			addPodHealth(podSummaries, workloadID, pod)
		}
		for ns, summary := range orphanByNS {
			nodes = append(nodes, CreateOrphanPodSummaryNode(ns, *summary, orphanRestarts[ns]))
		}
	} else if len(pods) > 0 {
		// Group pods using shared grouping logic
		groupingResult := GroupPods(pods, PodGroupingOptions{
			Namespaces: opts.Namespaces,
		})

		// Create nodes and edges for each group
		// Use MaxIndividualPods threshold to decide whether to show individual pods or group them
		maxIndividualPods := opts.MaxIndividualPods
		if maxIndividualPods <= 0 {
			maxIndividualPods = 5 // Default threshold
		}

		for _, group := range groupingResult.Groups {
			if len(group.Pods) <= maxIndividualPods {
				// Small group - add as individual nodes
				for _, pod := range group.Pods {
					podID := GetPodID(pod)
					nodes = append(nodes, CreatePodNode(pod, b.provider, true)) // includeNodeName=true for resources view

					// Connect to owner (resources view specific)
					edges = append(edges, b.createPodOwnerEdges(pod, podID, opts, replicaSetIDs, replicaSetToDeployment, replicaSetToRollout, jobIDs, jobToCronJob, jobToScaledJob, workflowIDs, workflowToCronWorkflow)...)
				}
			} else {
				// Large group - create PodGroup
				podGroupID := GetPodGroupID(group)
				nodes = append(nodes, CreatePodGroupNode(group, b.provider))

				// Connect to owner using first pod's owner (resources view specific)
				firstPod := group.Pods[0]
				edges = append(edges, b.createPodOwnerEdges(firstPod, podGroupID, opts, replicaSetIDs, replicaSetToDeployment, replicaSetToRollout, jobIDs, jobToCronJob, jobToScaledJob, workflowIDs, workflowToCronWorkflow)...)
			}
		}
	}

	// 8. Add Service nodes
	var services []*corev1.Service
	{
		svcs, svcsErr := b.provider.Services()
		if svcsErr != nil {
			log.Printf("WARNING [topology] Failed to list Services: %v", svcsErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list Services: %v", svcsErr))
		}
		services = svcs
	}

	// Pre-index workloads by namespace for faster service-to-workload matching
	// This avoids O(services × all_workloads) and instead does O(services × workloads_per_namespace)
	deploymentsByNS := make(map[string][]*appsv1.Deployment)
	for _, deploy := range deployments {
		deploymentsByNS[deploy.Namespace] = append(deploymentsByNS[deploy.Namespace], deploy)
	}
	statefulsetsByNS := make(map[string][]*appsv1.StatefulSet)
	for _, sts := range statefulsets {
		statefulsetsByNS[sts.Namespace] = append(statefulsetsByNS[sts.Namespace], sts)
	}
	daemonsetsByNS := make(map[string][]*appsv1.DaemonSet)
	for _, ds := range daemonsets {
		daemonsetsByNS[ds.Namespace] = append(daemonsetsByNS[ds.Namespace], ds)
	}

	for _, svc := range services {
		if !opts.MatchesNamespaceFilter(svc.Namespace) {
			continue
		}

		svcID := fmt.Sprintf("service/%s/%s", svc.Namespace, svc.Name)
		serviceIDs[svc.Namespace+"/"+svc.Name] = svcID

		var port int32
		if len(svc.Spec.Ports) > 0 {
			port = svc.Spec.Ports[0].Port
		}

		nodes = append(nodes, Node{
			ID:     svcID,
			Kind:   KindService,
			Name:   svc.Name,
			Status: StatusHealthy,
			Data: map[string]any{
				"namespace": svc.Namespace,
				"type":      string(svc.Spec.Type),
				"clusterIP": svc.Spec.ClusterIP,
				"port":      port,
				"labels":    svc.Labels,
			},
		})

		// Connect Service to Deployments via selector (using namespace-indexed lookup)
		if svc.Spec.Selector != nil {
			for _, deploy := range deploymentsByNS[svc.Namespace] {
				if matchesSelector(deploy.Spec.Template.ObjectMeta.Labels, svc.Spec.Selector) {
					deployID := deploymentIDs[deploy.Namespace+"/"+deploy.Name]
					edges = append(edges, Edge{
						ID:     fmt.Sprintf("%s-to-%s", svcID, deployID),
						Source: svcID,
						Target: deployID,
						Type:   EdgeExposes,
					})
				}
			}
		}
		// Check StatefulSets (using namespace-indexed lookup)
		for _, sts := range statefulsetsByNS[svc.Namespace] {
			if matchesSelector(sts.Spec.Template.ObjectMeta.Labels, svc.Spec.Selector) {
				stsID := statefulSetIDs[sts.Namespace+"/"+sts.Name]
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", svcID, stsID),
					Source: svcID,
					Target: stsID,
					Type:   EdgeExposes,
				})
			}
		}
		// Check DaemonSets (using namespace-indexed lookup)
		for _, ds := range daemonsetsByNS[svc.Namespace] {
			if matchesSelector(ds.Spec.Template.ObjectMeta.Labels, svc.Spec.Selector) {
				dsID := fmt.Sprintf("daemonset/%s/%s", ds.Namespace, ds.Name)
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", svcID, dsID),
					Source: svcID,
					Target: dsID,
					Type:   EdgeExposes,
				})
			}
		}
		// Check Rollouts (if we have any)
		if hasRollouts {
			for _, rollout := range rolloutsByNamespace[svc.Namespace] {
				spec, _, _ := unstructured.NestedMap(rollout.Object, "spec", "template", "metadata")
				if spec != nil {
					if podLabels, ok := spec["labels"].(map[string]any); ok {
						// Convert map[string]any to map[string]string for matching
						strLabels := make(map[string]string)
						for k, v := range podLabels {
							if s, ok := v.(string); ok {
								strLabels[k] = s
							}
						}
						if matchesSelector(strLabels, svc.Spec.Selector) {
							rolloutID := rolloutIDs[rollout.GetNamespace()+"/"+rollout.GetName()]
							if rolloutID != "" {
								edges = append(edges, Edge{
									ID:     fmt.Sprintf("%s-to-%s", svcID, rolloutID),
									Source: svcID,
									Target: rolloutID,
									Type:   EdgeExposes,
								})
							}
						}
					}
				}
			}
		}
		// Check Jobs
		for _, job := range jobs {
			if job.Namespace != svc.Namespace {
				continue
			}
			if job.Status.Active == 0 {
				continue
			}
			if matchesSelector(job.Spec.Template.ObjectMeta.Labels, svc.Spec.Selector) {
				jobID := jobIDs[job.Namespace+"/"+job.Name]
				if jobID != "" {
					edges = append(edges, Edge{
						ID:     fmt.Sprintf("%s-to-%s", svcID, jobID),
						Source: svcID,
						Target: jobID,
						Type:   EdgeExposes,
					})
				}
			}
		}
	}

	// 8b. Add Prometheus Operator monitor nodes. These are configuration
	// resources: ServiceMonitors select Services, while PodMonitors select pod
	// templates owned by workloads. Empty selectors remain visible but do not
	// fan out across the entire graph.
	if dynamicCache != nil && resourceDiscovery != nil {
		if gvr, ok := resourceDiscovery.GetGVRWithGroup("ServiceMonitor", "monitoring.coreos.com"); ok {
			monitors, monitorErr := dynamicCache.ListNamespaces(gvr, opts.Namespaces)
			if monitorErr != nil {
				log.Printf("WARNING [topology] Failed to list ServiceMonitors: %v", monitorErr)
				warnings = append(warnings, fmt.Sprintf("Failed to list ServiceMonitors: %v", monitorErr))
			}
			for _, monitor := range monitors {
				if !opts.MatchesNamespaceFilter(monitor.GetNamespace()) {
					continue
				}
				monitorID := fmt.Sprintf("servicemonitor/%s/%s", monitor.GetNamespace(), monitor.GetName())
				nodeData := monitorNodeData(monitor, "endpoints")
				nodes = append(nodes, Node{ID: monitorID, Kind: KindServiceMonitor, Name: monitor.GetName(), Status: StatusHealthy, Data: nodeData})

				selector, empty, selectorErr := monitorLabelSelector(monitor)
				if selectorErr != nil {
					warnings = append(warnings, fmt.Sprintf("ServiceMonitor %s/%s has an invalid selector: %v", monitor.GetNamespace(), monitor.GetName(), selectorErr))
					continue
				}
				if empty {
					nodeData["matchesAllTargets"] = true
					continue
				}
				for _, svc := range services {
					if !monitorSelectsNamespace(monitor, svc.Namespace) || !selector.Matches(labels.Set(svc.Labels)) {
						continue
					}
					if targetID := serviceIDs[svc.Namespace+"/"+svc.Name]; targetID != "" {
						edges = append(edges, Edge{ID: fmt.Sprintf("%s-to-%s", monitorID, targetID), Source: monitorID, Target: targetID, Type: EdgeConfigures})
					}
				}
			}
		}

		if gvr, ok := resourceDiscovery.GetGVRWithGroup("PodMonitor", "monitoring.coreos.com"); ok {
			monitors, monitorErr := dynamicCache.ListNamespaces(gvr, opts.Namespaces)
			if monitorErr != nil {
				log.Printf("WARNING [topology] Failed to list PodMonitors: %v", monitorErr)
				warnings = append(warnings, fmt.Sprintf("Failed to list PodMonitors: %v", monitorErr))
			}
			for _, monitor := range monitors {
				if !opts.MatchesNamespaceFilter(monitor.GetNamespace()) {
					continue
				}
				monitorID := fmt.Sprintf("podmonitor/%s/%s", monitor.GetNamespace(), monitor.GetName())
				nodeData := monitorNodeData(monitor, "podMetricsEndpoints")
				nodes = append(nodes, Node{ID: monitorID, Kind: KindPodMonitor, Name: monitor.GetName(), Status: StatusHealthy, Data: nodeData})

				selector, empty, selectorErr := monitorLabelSelector(monitor)
				if selectorErr != nil {
					warnings = append(warnings, fmt.Sprintf("PodMonitor %s/%s has an invalid selector: %v", monitor.GetNamespace(), monitor.GetName(), selectorErr))
					continue
				}
				if empty {
					nodeData["matchesAllTargets"] = true
					continue
				}
				addTarget := func(namespace string, targetLabels map[string]string, targetID string) {
					if targetID != "" && opts.MatchesNamespaceFilter(namespace) && monitorSelectsNamespace(monitor, namespace) && selector.Matches(labels.Set(targetLabels)) {
						edges = append(edges, Edge{ID: fmt.Sprintf("%s-to-%s", monitorID, targetID), Source: monitorID, Target: targetID, Type: EdgeConfigures})
					}
				}
				for _, deploy := range deployments {
					addTarget(deploy.Namespace, deploy.Spec.Template.Labels, deploymentIDs[deploy.Namespace+"/"+deploy.Name])
				}
				for _, sts := range statefulsets {
					addTarget(sts.Namespace, sts.Spec.Template.Labels, statefulSetIDs[sts.Namespace+"/"+sts.Name])
				}
				for _, ds := range daemonsets {
					addTarget(ds.Namespace, ds.Spec.Template.Labels, fmt.Sprintf("daemonset/%s/%s", ds.Namespace, ds.Name))
				}
				for _, job := range jobs {
					if job.Status.Active > 0 {
						addTarget(job.Namespace, job.Spec.Template.Labels, jobIDs[job.Namespace+"/"+job.Name])
					}
				}
				for _, cj := range cronjobs {
					addTarget(cj.Namespace, cj.Spec.JobTemplate.Spec.Template.Labels, cronJobIDs[cj.Namespace+"/"+cj.Name])
				}
				for _, rollout := range rolloutsByNamespace {
					for _, item := range rollout {
						podLabels, _, _ := unstructured.NestedStringMap(item.Object, "spec", "template", "metadata", "labels")
						addTarget(item.GetNamespace(), podLabels, rolloutIDs[item.GetNamespace()+"/"+item.GetName()])
					}
				}
			}
		}
	}

	// 7. Add Ingress nodes
	var ingresses []*networkingv1.Ingress
	{
		ings, ingsErr := b.provider.Ingresses()
		if ingsErr != nil {
			log.Printf("WARNING [topology] Failed to list Ingresses: %v", ingsErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list Ingresses: %v", ingsErr))
		}
		ingresses = ings
	}
	for _, ing := range ingresses {
		if !opts.MatchesNamespaceFilter(ing.Namespace) {
			continue
		}

		ingID := fmt.Sprintf("ingress/%s/%s", ing.Namespace, ing.Name)

		var host string
		if len(ing.Spec.Rules) > 0 && ing.Spec.Rules[0].Host != "" {
			host = ing.Spec.Rules[0].Host
		}

		hasTLS := len(ing.Spec.TLS) > 0

		nodes = append(nodes, Node{
			ID:     ingID,
			Kind:   KindIngress,
			Name:   ing.Name,
			Status: StatusHealthy,
			Data: map[string]any{
				"namespace": ing.Namespace,
				"hostname":  host,
				"tls":       hasTLS,
				"labels":    ing.Labels,
			},
		})

		// Connect to backend Services
		for _, rule := range ing.Spec.Rules {
			if rule.HTTP == nil {
				continue
			}
			for _, path := range rule.HTTP.Paths {
				if path.Backend.Service != nil {
					svcKey := ing.Namespace + "/" + path.Backend.Service.Name
					if svcID, ok := serviceIDs[svcKey]; ok {
						edges = append(edges, Edge{
							ID:     fmt.Sprintf("%s-to-%s", ingID, svcID),
							Source: ingID,
							Target: svcID,
							Type:   EdgeRoutesTo,
						})
					}
				}
			}
		}
	}

	// 7b. Add Gateway API nodes (CRD - fetched via dynamic cache)
	gatewayIDs := make(map[string]string)                  // ns/name -> gatewayID
	routeIDs := make(map[string]string)                    // kind/ns/name -> routeID
	var gatewayRouteResources []*unstructured.Unstructured // all routes for second-pass edge creation
	var gatewayRouteKinds []string                         // kind for each entry in gatewayRouteResources

	var gatewayGVR schema.GroupVersionResource
	hasGateways := false
	if resourceDiscovery != nil {
		gatewayGVR, hasGateways = resourceDiscovery.GetGVR("Gateway")
	}
	if hasGateways && dynamicCache != nil {
		gateways, gwErr := dynamicCache.ListNamespaces(gatewayGVR, opts.Namespaces)
		if gwErr != nil {
			log.Printf("WARNING [topology] Failed to list Gateways: %v", gwErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list Gateways: %v", gwErr))
		}
		for _, gw := range gateways {
			ns := gw.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := gw.GetName()
			gwID := fmt.Sprintf("gateway/%s/%s", ns, name)
			gatewayIDs[ns+"/"+name] = gwID

			listeners, _, _ := unstructured.NestedSlice(gw.Object, "spec", "listeners")
			addresses, _, _ := unstructured.NestedSlice(gw.Object, "status", "addresses")

			// Extract address values
			var addrList []string
			for _, addr := range addresses {
				if addrMap, ok := addr.(map[string]any); ok {
					if val, ok := addrMap["value"].(string); ok {
						addrList = append(addrList, val)
					}
				}
			}

			nodes = append(nodes, Node{
				ID:     gwID,
				Kind:   KindGateway,
				Name:   name,
				Status: getGatewayHealth(gw),
				Data: map[string]any{
					"namespace":     ns,
					"listenerCount": len(listeners),
					"addresses":     addrList,
					"labels":        gw.GetLabels(),
					"apiVersion":    gw.GetAPIVersion(),
				},
			})
		}
	}

	// Create GatewayClass → Gateway edges (match via spec.gatewayClassName on Gateway)
	if hasGateways && dynamicCache != nil {
		gateways, gwEdgeErr := dynamicCache.ListNamespaces(gatewayGVR, opts.Namespaces)
		if gwEdgeErr != nil {
			log.Printf("WARNING [topology] Failed to list Gateways for GatewayClass edges: %v", gwEdgeErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list Gateways for GatewayClass edges: %v", gwEdgeErr))
		}
		for _, gw := range gateways {
			ns := gw.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := gw.GetName()
			gwID := gatewayIDs[ns+"/"+name]
			if gwID == "" {
				continue
			}
			className, _, _ := unstructured.NestedString(gw.Object, "spec", "gatewayClassName")
			if className != "" {
				if gcID, ok := gatewayClassIDs[className]; ok {
					edges = append(edges, Edge{
						ID:     fmt.Sprintf("%s-to-%s", gcID, gwID),
						Source: gcID,
						Target: gwID,
						Type:   EdgeManages,
					})
				}
			}
		}
	}

	// Add Gateway API route nodes (HTTPRoute, GRPCRoute, TCPRoute, TLSRoute)
	gatewayRouteKindList := []string{"HTTPRoute", "GRPCRoute", "TCPRoute", "TLSRoute"}
	for _, routeKind := range gatewayRouteKindList {
		var routeGVR schema.GroupVersionResource
		hasRoutes := false
		if resourceDiscovery != nil {
			routeGVR, hasRoutes = resourceDiscovery.GetGVR(routeKind)
		}
		if !hasRoutes || dynamicCache == nil {
			continue
		}
		routes, routeErr := dynamicCache.ListNamespaces(routeGVR, opts.Namespaces)
		if routeErr != nil {
			log.Printf("WARNING [topology] Failed to list %s: %v", routeKind, routeErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list %s: %v", routeKind, routeErr))
			continue
		}
		for _, route := range routes {
			ns := route.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := route.GetName()
			kindLower := strings.ToLower(routeKind)
			routeID := fmt.Sprintf("%s/%s/%s", kindLower, ns, name)
			routeIDs[routeKind+"/"+ns+"/"+name] = routeID

			hostnames, _, _ := unstructured.NestedStringSlice(route.Object, "spec", "hostnames")
			rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "rules")

			nodes = append(nodes, Node{
				ID:     routeID,
				Kind:   NodeKind(routeKind),
				Name:   name,
				Status: getRouteHealth(route),
				Data: map[string]any{
					"namespace":  ns,
					"hostnames":  hostnames,
					"rulesCount": len(rules),
					"labels":     route.GetLabels(),
					"apiVersion": route.GetAPIVersion(),
				},
			})

			// Store for second-pass edge creation
			gatewayRouteResources = append(gatewayRouteResources, route)
			gatewayRouteKinds = append(gatewayRouteKinds, routeKind)
		}
	}

	// 8. Add ConfigMap nodes (if enabled)
	if opts.IncludeConfigMaps {
		configmaps, cmErr := b.provider.ConfigMaps()
		if cmErr != nil {
			log.Printf("WARNING [topology] Failed to list ConfigMaps: %v", cmErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list ConfigMaps: %v", cmErr))
		}
		cmConsumers := buildConsumerIndex(workloadConfigMapRefs, workloadNamespaces)
		for _, cm := range configmaps {
			if !opts.MatchesNamespaceFilter(cm.Namespace) {
				continue
			}

			// Only include ConfigMaps that are referenced by workloads in the same namespace
			cmID := fmt.Sprintf("configmap/%s/%s", cm.Namespace, cm.Name)
			consumers := cmConsumers[workloadRefKey{namespace: cm.Namespace, name: cm.Name}]

			for _, workloadID := range consumers {
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", cmID, workloadID),
					Source: cmID,
					Target: workloadID,
					Type:   EdgeConfigures,
				})
			}

			if len(consumers) > 0 {
				nodes = append(nodes, Node{
					ID:     cmID,
					Kind:   KindConfigMap,
					Name:   cm.Name,
					Status: StatusHealthy,
					Data: map[string]any{
						"namespace": cm.Namespace,
						"keys":      len(cm.Data),
						"labels":    cm.Labels,
					},
				})
			}
		}
	}

	// 9. Add Secret nodes (if enabled and RBAC permits)
	visibleSecretIDs := make(map[workloadRefKey]string)
	if opts.IncludeSecrets {
		secrets, secretsErr := b.provider.Secrets()
		if secretsErr != nil {
			log.Printf("WARNING [topology] Failed to list Secrets: %v", secretsErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list Secrets: %v", secretsErr))
		}
		secretConsumers := buildConsumerIndex(workloadSecretRefs, workloadNamespaces)
		for _, secret := range secrets {
			if !opts.MatchesNamespaceFilter(secret.Namespace) {
				continue
			}

			// Only include Secrets that are referenced by workloads in the same namespace
			secretID := fmt.Sprintf("secret/%s/%s", secret.Namespace, secret.Name)
			consumers := secretConsumers[workloadRefKey{namespace: secret.Namespace, name: secret.Name}]

			for _, workloadID := range consumers {
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", secretID, workloadID),
					Source: secretID,
					Target: workloadID,
					Type:   EdgeConfigures,
				})
			}

			if len(consumers) > 0 {
				visibleSecretIDs[workloadRefKey{namespace: secret.Namespace, name: secret.Name}] = secretID
				nodes = append(nodes, Node{
					ID:     secretID,
					Kind:   KindSecret,
					Name:   secret.Name,
					Status: StatusHealthy,
					Data: map[string]any{
						"namespace": secret.Namespace,
						"type":      string(secret.Type),
						"keys":      len(secret.Data),
						"labels":    secret.Labels,
					},
				})
			}
		}
	}

	// Workload identities are useful topology only when explicitly selected.
	if provider, ok := b.provider.(ServiceAccountProvider); ok {
		serviceAccounts, serviceAccountsErr := provider.ServiceAccounts()
		if serviceAccountsErr != nil {
			log.Printf("WARNING [topology] Failed to list ServiceAccounts: %v", serviceAccountsErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list ServiceAccounts: %v", serviceAccountsErr))
		}
		consumersByServiceAccount := make(map[workloadRefKey][]string)
		for workloadID, name := range workloadServiceAccountRefs {
			key := workloadRefKey{namespace: workloadNamespaces[workloadID], name: name}
			consumersByServiceAccount[key] = append(consumersByServiceAccount[key], workloadID)
		}
		for _, serviceAccount := range serviceAccounts {
			if !opts.MatchesNamespaceFilter(serviceAccount.Namespace) {
				continue
			}
			key := workloadRefKey{namespace: serviceAccount.Namespace, name: serviceAccount.Name}
			consumers := consumersByServiceAccount[key]
			if len(consumers) == 0 {
				continue
			}
			serviceAccountID := fmt.Sprintf("serviceaccount/%s/%s", serviceAccount.Namespace, serviceAccount.Name)
			nodes = append(nodes, Node{
				ID: serviceAccountID, Kind: KindServiceAccount, Name: serviceAccount.Name, Status: StatusHealthy,
				Data: map[string]any{"namespace": serviceAccount.Namespace, "labels": serviceAccount.Labels},
			})
			for _, workloadID := range consumers {
				edges = append(edges, Edge{
					ID: fmt.Sprintf("%s-to-%s", serviceAccountID, workloadID), Source: serviceAccountID, Target: workloadID, Type: EdgeConfigures,
				})
			}
		}
	}

	// SealedSecrets are linked through the Secret name they materialize. Direct
	// consumer edges preserve the relationship when Secret reads are forbidden.
	if dynamicCache != nil && resourceDiscovery != nil {
		if sealedSecretGVR, ok := resourceDiscovery.GetGVRWithGroup("SealedSecret", "bitnami.com"); ok {
			sealedSecrets, sealedSecretsErr := dynamicCache.ListNamespaces(sealedSecretGVR, opts.Namespaces)
			if sealedSecretsErr != nil {
				log.Printf("WARNING [topology] Failed to list SealedSecrets: %v", sealedSecretsErr)
				warnings = append(warnings, fmt.Sprintf("Failed to list SealedSecrets: %v", sealedSecretsErr))
			}
			secretConsumers := buildConsumerIndex(workloadSecretRefs, workloadNamespaces)
			for _, sealedSecret := range sealedSecrets {
				namespace := sealedSecret.GetNamespace()
				if !opts.MatchesNamespaceFilter(namespace) {
					continue
				}
				targetName, _, _ := unstructured.NestedString(sealedSecret.Object, "spec", "template", "metadata", "name")
				if targetName == "" {
					targetName = sealedSecret.GetName()
				}
				key := workloadRefKey{namespace: namespace, name: targetName}
				consumers := secretConsumers[key]
				if len(consumers) == 0 {
					continue
				}
				sealedSecretID := fmt.Sprintf("sealedsecret/%s/%s", namespace, sealedSecret.GetName())
				nodes = append(nodes, Node{
					ID: sealedSecretID, Kind: KindSealedSecret, Name: sealedSecret.GetName(), Status: sealedSecretHealth(sealedSecret),
					Data: map[string]any{"namespace": namespace, "targetSecret": targetName, "labels": sealedSecret.GetLabels(), "apiVersion": sealedSecret.GetAPIVersion()},
				})
				if secretID := visibleSecretIDs[key]; secretID != "" {
					edges = append(edges, Edge{ID: fmt.Sprintf("%s-to-%s", sealedSecretID, secretID), Source: sealedSecretID, Target: secretID, Type: EdgeManages})
					continue
				}
				for _, workloadID := range consumers {
					edges = append(edges, Edge{ID: fmt.Sprintf("%s-to-%s", sealedSecretID, workloadID), Source: sealedSecretID, Target: workloadID, Type: EdgeConfigures})
				}
			}
		}
	}

	// 10. Add PVC nodes (if enabled)
	if opts.IncludePVCs {
		pvcs, pvcErr := b.provider.PersistentVolumeClaims()
		if pvcErr != nil {
			log.Printf("WARNING [topology] Failed to list PersistentVolumeClaims: %v", pvcErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list PersistentVolumeClaims: %v", pvcErr))
		}
		pvcConsumers := buildConsumerIndex(workloadPVCRefs, workloadNamespaces)
		for _, pvc := range pvcs {
			if !opts.MatchesNamespaceFilter(pvc.Namespace) {
				continue
			}

			// Only include PVCs that are referenced by workloads in the same namespace
			pvcID := fmt.Sprintf("persistentvolumeclaim/%s/%s", pvc.Namespace, pvc.Name)
			consumers := pvcConsumers[workloadRefKey{namespace: pvc.Namespace, name: pvc.Name}]

			for _, workloadID := range consumers {
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", pvcID, workloadID),
					Source: pvcID,
					Target: workloadID,
					Type:   EdgeUses,
				})
			}

			if len(consumers) > 0 {
				// Get storage info
				var storageSize string
				if pvc.Spec.Resources.Requests != nil {
					if storage, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
						storageSize = storage.String()
					}
				}

				var storageClass string
				if pvc.Spec.StorageClassName != nil {
					storageClass = *pvc.Spec.StorageClassName
				}

				nodes = append(nodes, Node{
					ID:     pvcID,
					Kind:   KindPVC,
					Name:   pvc.Name,
					Status: getPVCStatus(pvc),
					Data: map[string]any{
						"namespace":    pvc.Namespace,
						"storageClass": storageClass,
						"accessModes":  pvc.Spec.AccessModes,
						"storage":      storageSize,
						"phase":        string(pvc.Status.Phase),
						"labels":       pvc.Labels,
					},
				})
			}
		}
	}

	// 11. Add HPA nodes
	hpas, hpaErr := b.provider.HorizontalPodAutoscalers()
	if hpaErr != nil {
		log.Printf("WARNING [topology] Failed to list HorizontalPodAutoscalers: %v", hpaErr)
		warnings = append(warnings, fmt.Sprintf("Failed to list HorizontalPodAutoscalers: %v", hpaErr))
	}
	for _, hpa := range hpas {
		if !opts.MatchesNamespaceFilter(hpa.Namespace) {
			continue
		}

		hpaID := fmt.Sprintf("horizontalpodautoscaler/%s/%s", hpa.Namespace, hpa.Name)

		nodes = append(nodes, Node{
			ID:     hpaID,
			Kind:   KindHPA,
			Name:   hpa.Name,
			Status: StatusHealthy,
			Data: map[string]any{
				"namespace":   hpa.Namespace,
				"minReplicas": hpa.Spec.MinReplicas,
				"maxReplicas": hpa.Spec.MaxReplicas,
				"current":     hpa.Status.CurrentReplicas,
				"labels":      hpa.Labels,
			},
		})

		// Connect to target
		targetKind := hpa.Spec.ScaleTargetRef.Kind
		targetName := hpa.Spec.ScaleTargetRef.Name
		targetKey := hpa.Namespace + "/" + targetName

		var targetID string
		switch targetKind {
		case "Deployment":
			targetID = deploymentIDs[targetKey]
		case "Rollout":
			targetID = rolloutIDs[targetKey]
		case "StatefulSet":
			targetID = statefulSetIDs[targetKey]
		case "ReplicaSet":
			targetID = replicaSetIDs[targetKey]
		}

		if targetID != "" {
			edges = append(edges, Edge{
				ID:     fmt.Sprintf("%s-to-%s", hpaID, targetID),
				Source: hpaID,
				Target: targetID,
				Type:   EdgeUses,
			})
		}
	}

	// 11b. Add PDB nodes
	pdbs, pdbErr := b.provider.PodDisruptionBudgets()
	if pdbErr != nil {
		log.Printf("WARNING [topology] Failed to list PodDisruptionBudgets: %v", pdbErr)
		warnings = append(warnings, fmt.Sprintf("Failed to list PodDisruptionBudgets: %v", pdbErr))
	}
	for _, pdb := range pdbs {
		if !opts.MatchesNamespaceFilter(pdb.Namespace) {
			continue
		}

		pdbID := fmt.Sprintf("poddisruptionbudget/%s/%s", pdb.Namespace, pdb.Name)

		status := StatusHealthy
		if pdb.Status.DisruptionsAllowed == 0 && pdb.Status.CurrentHealthy < pdb.Status.DesiredHealthy {
			status = StatusDegraded
		}

		nodes = append(nodes, Node{
			ID:     pdbID,
			Kind:   KindPDB,
			Name:   pdb.Name,
			Status: status,
			Data: map[string]any{
				"namespace":          pdb.Namespace,
				"disruptionsAllowed": pdb.Status.DisruptionsAllowed,
				"currentHealthy":     pdb.Status.CurrentHealthy,
				"desiredHealthy":     pdb.Status.DesiredHealthy,
				"labels":             pdb.Labels,
			},
		})

		// Connect to target workloads by matching PDB's selector against workload pod template labels
		if pdb.Spec.Selector != nil {
			sel, selErr := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
			if selErr == nil {
				// Check Deployments
				for _, d := range deployments {
					if d.Namespace != pdb.Namespace {
						continue
					}
					if sel.Matches(labels.Set(d.Spec.Template.Labels)) {
						targetID := deploymentIDs[d.Namespace+"/"+d.Name]
						if targetID != "" {
							edges = append(edges, Edge{
								ID:     fmt.Sprintf("%s-to-%s", pdbID, targetID),
								Source: pdbID,
								Target: targetID,
								Type:   EdgeProtects,
							})
						}
					}
				}
				// Check StatefulSets
				for _, s := range statefulsets {
					if s.Namespace != pdb.Namespace {
						continue
					}
					if sel.Matches(labels.Set(s.Spec.Template.Labels)) {
						targetID := statefulSetIDs[s.Namespace+"/"+s.Name]
						if targetID != "" {
							edges = append(edges, Edge{
								ID:     fmt.Sprintf("%s-to-%s", pdbID, targetID),
								Source: pdbID,
								Target: targetID,
								Type:   EdgeProtects,
							})
						}
					}
				}
				// Check DaemonSets
				for _, d := range daemonsets {
					if d.Namespace != pdb.Namespace {
						continue
					}
					if sel.Matches(labels.Set(d.Spec.Template.Labels)) {
						dsID := fmt.Sprintf("daemonset/%s/%s", d.Namespace, d.Name)
						edges = append(edges, Edge{
							ID:     fmt.Sprintf("%s-to-%s", pdbID, dsID),
							Source: pdbID,
							Target: dsID,
							Type:   EdgeProtects,
						})
					}
				}
			}
		}
	}

	// 11c. Add NetworkPolicy nodes
	netpols, npErr := b.provider.NetworkPolicies()
	if npErr != nil {
		log.Printf("WARNING [topology] Failed to list NetworkPolicies: %v", npErr)
		warnings = append(warnings, fmt.Sprintf("Failed to list NetworkPolicies: %v", npErr))
	}
	for _, np := range netpols {
		if !opts.MatchesNamespaceFilter(np.Namespace) {
			continue
		}

		npID := fmt.Sprintf("networkpolicy/%s/%s", np.Namespace, np.Name)

		policyTypes := make([]string, 0, 2)
		for _, pt := range np.Spec.PolicyTypes {
			policyTypes = append(policyTypes, string(pt))
		}

		nodeData := map[string]any{
			"namespace":   np.Namespace,
			"policyTypes": policyTypes,
			"labels":      np.Labels,
		}

		nodes = append(nodes, Node{
			ID:     npID,
			Kind:   KindNetworkPolicy,
			Name:   np.Name,
			Status: StatusHealthy,
			Data:   nodeData,
		})

		// Connect to target workloads by matching policy's podSelector against workload pod template labels.
		// Empty selector (matches all pods) skips edges to avoid topology clutter from default-deny policies.
		if np.Spec.PodSelector.Size() == 0 {
			nodeData["matchesAllPods"] = true
			continue
		}

		sel, selErr := metav1.LabelSelectorAsSelector(&np.Spec.PodSelector)
		if selErr != nil {
			continue
		}

		for _, d := range deployments {
			if d.Namespace != np.Namespace {
				continue
			}
			if sel.Matches(labels.Set(d.Spec.Template.Labels)) {
				if targetID := deploymentIDs[d.Namespace+"/"+d.Name]; targetID != "" {
					edges = append(edges, Edge{
						ID:     fmt.Sprintf("%s-to-%s", npID, targetID),
						Source: npID,
						Target: targetID,
						Type:   EdgeProtects,
					})
				}
			}
		}
		for _, s := range statefulsets {
			if s.Namespace != np.Namespace {
				continue
			}
			if sel.Matches(labels.Set(s.Spec.Template.Labels)) {
				if targetID := statefulSetIDs[s.Namespace+"/"+s.Name]; targetID != "" {
					edges = append(edges, Edge{
						ID:     fmt.Sprintf("%s-to-%s", npID, targetID),
						Source: npID,
						Target: targetID,
						Type:   EdgeProtects,
					})
				}
			}
		}
		for _, d := range daemonsets {
			if d.Namespace != np.Namespace {
				continue
			}
			if sel.Matches(labels.Set(d.Spec.Template.Labels)) {
				dsID := fmt.Sprintf("daemonset/%s/%s", d.Namespace, d.Name)
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", npID, dsID),
					Source: npID,
					Target: dsID,
					Type:   EdgeProtects,
				})
			}
		}
	}

	// 11d. Add CiliumNetworkPolicy nodes (CRD - fetched via dynamic cache)
	var cnpGVR schema.GroupVersionResource
	hasCNPs := false
	if resourceDiscovery != nil {
		cnpGVR, hasCNPs = resourceDiscovery.GetGVR("CiliumNetworkPolicy")
	}
	if hasCNPs && dynamicCache != nil {
		cnps, cnpErr := dynamicCache.ListNamespaces(cnpGVR, opts.Namespaces)
		if cnpErr != nil {
			log.Printf("WARNING [topology] Failed to list CiliumNetworkPolicies: %v", cnpErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list CiliumNetworkPolicies: %v", cnpErr))
		}
		for _, cnp := range cnps {
			ns := cnp.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := cnp.GetName()
			cnpID := fmt.Sprintf("ciliumnetworkpolicy/%s/%s", ns, name)

			nodeData := map[string]any{
				"namespace":  ns,
				"labels":     cnp.GetLabels(),
				"apiVersion": cnp.GetAPIVersion(),
			}

			nodes = append(nodes, Node{
				ID:     cnpID,
				Kind:   KindCiliumNetworkPolicy,
				Name:   name,
				Status: StatusHealthy,
				Data:   nodeData,
			})

			// Connect via endpointSelector (equivalent to podSelector)
			selectorMap, _, _ := unstructured.NestedMap(cnp.Object, "spec", "endpointSelector", "matchLabels")
			if len(selectorMap) == 0 {
				nodeData["matchesAllPods"] = true
				continue
			}

			for _, d := range deployments {
				if d.Namespace != ns {
					continue
				}
				if matchesStringMap(d.Spec.Template.Labels, selectorMap) {
					if targetID := deploymentIDs[d.Namespace+"/"+d.Name]; targetID != "" {
						edges = append(edges, Edge{
							ID: fmt.Sprintf("%s-to-%s", cnpID, targetID), Source: cnpID, Target: targetID, Type: EdgeProtects,
						})
					}
				}
			}
			for _, s := range statefulsets {
				if s.Namespace != ns {
					continue
				}
				if matchesStringMap(s.Spec.Template.Labels, selectorMap) {
					if targetID := statefulSetIDs[s.Namespace+"/"+s.Name]; targetID != "" {
						edges = append(edges, Edge{
							ID: fmt.Sprintf("%s-to-%s", cnpID, targetID), Source: cnpID, Target: targetID, Type: EdgeProtects,
						})
					}
				}
			}
			for _, d := range daemonsets {
				if d.Namespace != ns {
					continue
				}
				if matchesStringMap(d.Spec.Template.Labels, selectorMap) {
					dsID := fmt.Sprintf("daemonset/%s/%s", d.Namespace, d.Name)
					edges = append(edges, Edge{
						ID: fmt.Sprintf("%s-to-%s", cnpID, dsID), Source: cnpID, Target: dsID, Type: EdgeProtects,
					})
				}
			}
		}
	}

	// 11e. Add CiliumClusterwideNetworkPolicy nodes (CRD - cluster-scoped)
	var ccnpGVR schema.GroupVersionResource
	hasCCNPs := false
	if resourceDiscovery != nil {
		ccnpGVR, hasCCNPs = resourceDiscovery.GetGVR("CiliumClusterwideNetworkPolicy")
	}
	if hasCCNPs && dynamicCache != nil {
		ccnps, ccnpErr := dynamicCache.List(ccnpGVR, "")
		if ccnpErr != nil {
			log.Printf("WARNING [topology] Failed to list CiliumClusterwideNetworkPolicies: %v", ccnpErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list CiliumClusterwideNetworkPolicies: %v", ccnpErr))
		}
		for _, ccnp := range ccnps {
			name := ccnp.GetName()
			ccnpID := fmt.Sprintf("ciliumclusterwidenetworkpolicy/%s", name)

			nodes = append(nodes, Node{
				ID:     ccnpID,
				Kind:   KindCiliumClusterwideNetworkPolicy,
				Name:   name,
				Status: StatusHealthy,
				Data: map[string]any{
					"labels":     ccnp.GetLabels(),
					"apiVersion": ccnp.GetAPIVersion(),
				},
			})
			// Cluster-wide policies use endpointSelector or nodeSelector across all namespaces.
			// Edges are complex (cross-namespace matching) — skip for now, node presence is the value.
		}
	}

	// 11f. Add ClusterNetworkPolicy nodes (CRD - cluster-scoped, policy.networking.k8s.io)
	var cnpolicyGVR schema.GroupVersionResource
	hasCNPolicies := false
	if resourceDiscovery != nil {
		cnpolicyGVR, hasCNPolicies = resourceDiscovery.GetGVRWithGroup("ClusterNetworkPolicy", "policy.networking.k8s.io")
	}
	if hasCNPolicies && dynamicCache != nil {
		cnpolicies, cnpErr := dynamicCache.List(cnpolicyGVR, "")
		if cnpErr != nil {
			log.Printf("WARNING [topology] Failed to list ClusterNetworkPolicies: %v", cnpErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list ClusterNetworkPolicies: %v", cnpErr))
		}
		for _, cnp := range cnpolicies {
			name := cnp.GetName()
			cnpID := fmt.Sprintf("clusternetworkpolicy/%s", name)

			nodes = append(nodes, Node{
				ID:     cnpID,
				Kind:   KindClusterNetworkPolicy,
				Name:   name,
				Status: StatusHealthy,
				Data: map[string]any{
					"labels":     cnp.GetLabels(),
					"apiVersion": cnp.GetAPIVersion(),
				},
			})
			// ClusterNetworkPolicy uses spec.subject with namespace/pod selectors.
			// Cross-namespace matching is complex — skip edges for now, node presence is the value.
		}
	}

	// 11g. Add VPA nodes (CRD - fetched via dynamic cache)
	var vpaGVR schema.GroupVersionResource
	hasVPAs := false
	if resourceDiscovery != nil {
		vpaGVR, hasVPAs = resourceDiscovery.GetGVR("VerticalPodAutoscaler")
	}
	if hasVPAs && dynamicCache != nil {
		vpas, vpaErr := dynamicCache.ListNamespaces(vpaGVR, opts.Namespaces)
		if vpaErr != nil {
			log.Printf("WARNING [topology] Failed to list VerticalPodAutoscalers: %v", vpaErr)
			warnings = append(warnings, fmt.Sprintf("Failed to list VerticalPodAutoscalers: %v", vpaErr))
		}
		for _, vpa := range vpas {
			ns := vpa.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}
			name := vpa.GetName()
			vpaID := fmt.Sprintf("verticalpodautoscaler/%s/%s", ns, name)

			nodes = append(nodes, Node{
				ID:     vpaID,
				Kind:   KindVPA,
				Name:   name,
				Status: StatusHealthy,
				Data: map[string]any{
					"namespace":  ns,
					"labels":     vpa.GetLabels(),
					"apiVersion": vpa.GetAPIVersion(),
				},
			})

			// Connect to target workload via spec.targetRef
			targetKind, _, _ := unstructured.NestedString(vpa.Object, "spec", "targetRef", "kind")
			targetName, _, _ := unstructured.NestedString(vpa.Object, "spec", "targetRef", "name")
			if targetKind != "" && targetName != "" {
				targetKey := ns + "/" + targetName
				var targetID string
				switch targetKind {
				case "Deployment":
					targetID = deploymentIDs[targetKey]
				case "StatefulSet":
					targetID = statefulSetIDs[targetKey]
				case "DaemonSet":
					targetID = fmt.Sprintf("daemonset/%s/%s", ns, targetName)
				case "ReplicaSet":
					targetID = replicaSetIDs[targetKey]
				case "Rollout":
					targetID = rolloutIDs[targetKey]
				}
				if targetID != "" {
					edges = append(edges, Edge{
						ID:     fmt.Sprintf("%s-to-%s", vpaID, targetID),
						Source: vpaID,
						Target: targetID,
						Type:   EdgeUses,
					})
				}
			}
		}
	}

	// 12. Second pass: Create ArgoCD Application edges to managed resources
	// This is done after all resource IDs are populated
	for _, app := range applicationResources {
		ns := app.GetNamespace()
		name := app.GetName()
		appID := applicationIDs[ns+"/"+name]
		destNamespace := applicationDestNamespaces[appID]

		status, _, _ := unstructured.NestedMap(app.Object, "status")
		if status == nil {
			continue
		}

		resources, _, _ := unstructured.NestedSlice(status, "resources")
		for _, res := range resources {
			resMap, ok := res.(map[string]any)
			if !ok {
				continue
			}
			resKind, _ := resMap["kind"].(string)
			resName, _ := resMap["name"].(string)
			resNS, _ := resMap["namespace"].(string)
			if resNS == "" {
				resNS = destNamespace
			}

			// Build target ID based on kind
			var targetID string
			resKey := resNS + "/" + resName
			switch resKind {
			case "Deployment":
				targetID = deploymentIDs[resKey]
			case "StatefulSet":
				targetID = statefulSetIDs[resKey]
			case "DaemonSet":
				targetID = fmt.Sprintf("daemonset/%s/%s", resNS, resName)
			case "Service":
				targetID = serviceIDs[resKey]
			case "Rollout":
				targetID = rolloutIDs[resKey]
			case "Job":
				targetID = jobIDs[resKey]
			case "CronJob":
				targetID = cronJobIDs[resKey]
			case "Gateway":
				targetID = gatewayIDs[resKey]
			case "HTTPRoute", "GRPCRoute", "TCPRoute", "TLSRoute":
				targetID = routeIDs[resKind+"/"+resNS+"/"+resName]
			}

			// Only create edge if target exists in current cluster view
			if targetID != "" {
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", appID, targetID),
					Source: appID,
					Target: targetID,
					Type:   EdgeManages,
				})
			}
		}
	}

	// 13. Second pass: Create FluxCD Kustomization edges to managed resources
	// Kustomization inventory contains refs like "Deployment/ns/name" or "_namespace_name_Kind"
	for _, ks := range kustomizationResources {
		ns := ks.GetNamespace()
		name := ks.GetName()
		ksID := kustomizationIDs[ns+"/"+name]

		status, _, _ := unstructured.NestedMap(ks.Object, "status")
		if status == nil {
			continue
		}

		inventory, _, _ := unstructured.NestedSlice(status, "inventory", "entries")
		for _, entry := range inventory {
			entryMap, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			// FluxCD inventory entry has "id" field with format "namespace_name_group_kind" or "id" field
			entryID, _ := entryMap["id"].(string)
			if entryID == "" {
				continue
			}

			// Parse the inventory ID (format: namespace_name_group_kind)
			// Example: "default_my-deployment_apps_Deployment"
			parts := strings.Split(entryID, "_")
			if len(parts) < 3 {
				continue
			}

			resNS := parts[0]
			resName := parts[1]
			// Last part is kind, second to last is group (might be empty)
			resKind := parts[len(parts)-1]

			// Build target ID based on kind
			var targetID string
			resKey := resNS + "/" + resName
			switch resKind {
			case "Deployment":
				targetID = deploymentIDs[resKey]
			case "StatefulSet":
				targetID = statefulSetIDs[resKey]
			case "DaemonSet":
				targetID = fmt.Sprintf("daemonset/%s/%s", resNS, resName)
			case "Service":
				targetID = serviceIDs[resKey]
			case "Rollout":
				targetID = rolloutIDs[resKey]
			case "Job":
				targetID = jobIDs[resKey]
			case "CronJob":
				targetID = cronJobIDs[resKey]
			case "Ingress":
				targetID = fmt.Sprintf("ingress/%s/%s", resNS, resName)
			case "Gateway":
				targetID = gatewayIDs[resKey]
			case "HTTPRoute", "GRPCRoute", "TCPRoute", "TLSRoute":
				targetID = routeIDs[resKind+"/"+resNS+"/"+resName]
			}

			// Only create edge if target exists in current cluster view
			if targetID != "" {
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", ksID, targetID),
					Source: ksID,
					Target: targetID,
					Type:   EdgeManages,
				})
			}
		}

		// Also create edge from GitRepository to Kustomization if source ref exists
		spec, _, _ := unstructured.NestedMap(ks.Object, "spec")
		if spec != nil {
			if sourceRef, ok, _ := unstructured.NestedMap(spec, "sourceRef"); ok && sourceRef != nil {
				refKind, _ := sourceRef["kind"].(string)
				refName, _ := sourceRef["name"].(string)
				refNS, _ := sourceRef["namespace"].(string)
				if refNS == "" {
					refNS = ns // Default to same namespace
				}

				if refKind == "GitRepository" {
					gitRepoID := gitRepoIDs[refNS+"/"+refName]
					if gitRepoID != "" {
						edges = append(edges, Edge{
							ID:     fmt.Sprintf("%s-to-%s", gitRepoID, ksID),
							Source: gitRepoID,
							Target: ksID,
							Type:   EdgeManages, // GitRepo provides source for Kustomization
						})
					}
				}
			}
		}
	}

	// 14. Create FluxCD HelmRelease edges to managed resources
	// HelmReleases don't have inventory - match by labels:
	// - helm.toolkit.fluxcd.io/name (FluxCD-specific, preferred)
	// - app.kubernetes.io/instance (standard Helm label)
	for hrKey, hrID := range helmReleaseIDs {
		parts := strings.Split(hrKey, "/")
		if len(parts) != 2 {
			continue
		}
		hrNS := parts[0]
		hrName := parts[1]

		// Find Deployments with matching label
		for depKey, depID := range deploymentIDs {
			depParts := strings.Split(depKey, "/")
			if len(depParts) != 2 {
				continue
			}
			depNS := depParts[0]
			depName := depParts[1]

			// Must be in same namespace
			if depNS != hrNS {
				continue
			}

			// Check if deployment has matching label
			var dep *appsv1.Deployment
			for _, d := range deployments {
				if d.Namespace == depNS && d.Name == depName {
					dep = d
					break
				}
			}
			if dep == nil {
				continue
			}

			if matchesHelmRelease(dep.Labels, hrName, hrNS) {
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", hrID, depID),
					Source: hrID,
					Target: depID,
					Type:   EdgeManages,
				})
			}
		}

		// Find Services with matching label
		for svcKey, svcID := range serviceIDs {
			svcParts := strings.Split(svcKey, "/")
			if len(svcParts) != 2 {
				continue
			}
			svcNS := svcParts[0]
			svcName := svcParts[1]

			// Must be in same namespace
			if svcNS != hrNS {
				continue
			}

			// Check if service has matching label
			var svc *corev1.Service
			for _, s := range services {
				if s.Namespace == svcNS && s.Name == svcName {
					svc = s
					break
				}
			}
			if svc == nil {
				continue
			}

			if matchesHelmRelease(svc.Labels, hrName, hrNS) {
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", hrID, svcID),
					Source: hrID,
					Target: svcID,
					Type:   EdgeManages,
				})
			}
		}

		// Find StatefulSets with matching label
		for stsKey, stsID := range statefulSetIDs {
			stsParts := strings.Split(stsKey, "/")
			if len(stsParts) != 2 {
				continue
			}
			stsNS := stsParts[0]
			stsName := stsParts[1]

			// Must be in same namespace
			if stsNS != hrNS {
				continue
			}

			// Check if statefulset has matching label
			var sts *appsv1.StatefulSet
			for _, s := range statefulsets {
				if s.Namespace == stsNS && s.Name == stsName {
					sts = s
					break
				}
			}
			if sts == nil {
				continue
			}

			if matchesHelmRelease(sts.Labels, hrName, hrNS) {
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", hrID, stsID),
					Source: hrID,
					Target: stsID,
					Type:   EdgeManages,
				})
			}
		}

		// Find DaemonSets with matching label
		for _, ds := range daemonsetsByNS[hrNS] {
			if matchesHelmRelease(ds.Labels, hrName, hrNS) {
				dsID := fmt.Sprintf("daemonset/%s/%s", ds.Namespace, ds.Name)
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", hrID, dsID),
					Source: hrID,
					Target: dsID,
					Type:   EdgeManages,
				})
			}
		}

		// Find Jobs with matching label
		for jobKey, jobID := range jobIDs {
			jobParts := strings.Split(jobKey, "/")
			if len(jobParts) != 2 || jobParts[0] != hrNS {
				continue
			}
			var job *batchv1.Job
			for _, j := range jobs {
				if j.Namespace == jobParts[0] && j.Name == jobParts[1] {
					job = j
					break
				}
			}
			if job == nil {
				continue
			}
			if matchesHelmRelease(job.Labels, hrName, hrNS) {
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", hrID, jobID),
					Source: hrID,
					Target: jobID,
					Type:   EdgeManages,
				})
			}
		}

		// Find CronJobs with matching label
		for cjKey, cjID := range cronJobIDs {
			cjParts := strings.Split(cjKey, "/")
			if len(cjParts) != 2 || cjParts[0] != hrNS {
				continue
			}
			var cj *batchv1.CronJob
			for _, c := range cronjobs {
				if c.Namespace == cjParts[0] && c.Name == cjParts[1] {
					cj = c
					break
				}
			}
			if cj == nil {
				continue
			}
			if matchesHelmRelease(cj.Labels, hrName, hrNS) {
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", hrID, cjID),
					Source: hrID,
					Target: cjID,
					Type:   EdgeManages,
				})
			}
		}

		// Find Rollouts with matching label
		if hasRollouts && dynamicCache != nil {
			for rolloutKey, rolloutID := range rolloutIDs {
				rolloutParts := strings.Split(rolloutKey, "/")
				if len(rolloutParts) != 2 || rolloutParts[0] != hrNS {
					continue
				}
				rolloutRes, rolloutGetErr := dynamicCache.Get(rolloutGVR, rolloutParts[0], rolloutParts[1])
				if rolloutGetErr != nil || rolloutRes == nil {
					continue
				}
				if matchesHelmRelease(rolloutRes.GetLabels(), hrName, hrNS) {
					edges = append(edges, Edge{
						ID:     fmt.Sprintf("%s-to-%s", hrID, rolloutID),
						Source: hrID,
						Target: rolloutID,
						Type:   EdgeManages,
					})
				}
			}
		}
	}

	// 14b. Create Istio VirtualService edges
	// VirtualService → Service (via spec.http[].route[].destination.host)
	// Istio Gateway → VirtualService (via spec.gateways[])
	// Track seen VS→Service pairs to deduplicate (multiple routes to same service)
	vsToSvcSeen := make(map[string]bool)
	for _, vs := range virtualServiceResources {
		vsNs := vs.GetNamespace()
		vsName := vs.GetName()
		vsID := virtualServiceIDs[vsNs+"/"+vsName]

		spec, _, _ := unstructured.NestedMap(vs.Object, "spec")
		if spec == nil {
			continue
		}

		// Create Istio Gateway → VirtualService edges
		vsGateways, _, _ := unstructured.NestedStringSlice(spec, "gateways")
		for _, gwRef := range vsGateways {
			if gwRef == "mesh" {
				continue // "mesh" is a special keyword meaning sidecar-to-sidecar
			}
			// Gateway ref can be "namespace/name" or just "name" (same namespace)
			gwNs := vsNs
			gwName := gwRef
			if parts := strings.SplitN(gwRef, "/", 2); len(parts) == 2 {
				gwNs = parts[0]
				gwName = parts[1]
			}
			if igwID, ok := istioGatewayIDs[gwNs+"/"+gwName]; ok {
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", igwID, vsID),
					Source: igwID,
					Target: vsID,
					Type:   EdgeExposes,
				})
			}
		}

		// addVSToSvcEdge is a helper that deduplicates VS→Service edges
		addVSToSvcEdge := func(protocol string, destHost string) {
			svcName, svcNs := parseIstioHost(destHost, vsNs)
			svcID, ok := serviceIDs[svcNs+"/"+svcName]
			if !ok {
				return
			}
			dedupeKey := vsID + "|" + svcID
			if vsToSvcSeen[dedupeKey] {
				return
			}
			vsToSvcSeen[dedupeKey] = true
			suffix := ""
			if protocol != "" {
				suffix = "-" + protocol
			}
			edges = append(edges, Edge{
				ID:     fmt.Sprintf("%s%s-to-%s", vsID, suffix, svcID),
				Source: vsID,
				Target: svcID,
				Type:   EdgeExposes,
			})
		}

		// Create VirtualService → Service edges (via HTTP route destinations)
		httpRoutes, _, _ := unstructured.NestedSlice(spec, "http")
		for _, httpRoute := range httpRoutes {
			routeMap, ok := httpRoute.(map[string]any)
			if !ok {
				continue
			}
			routeDestinations, _, _ := unstructured.NestedSlice(routeMap, "route")
			for _, rd := range routeDestinations {
				rdMap, ok := rd.(map[string]any)
				if !ok {
					continue
				}
				destHost, _, _ := unstructured.NestedString(rdMap, "destination", "host")
				if destHost != "" {
					addVSToSvcEdge("", destHost)
				}
			}
		}

		// Also handle TCP route destinations
		tcpRoutes, _, _ := unstructured.NestedSlice(spec, "tcp")
		for _, tcpRoute := range tcpRoutes {
			routeMap, ok := tcpRoute.(map[string]any)
			if !ok {
				continue
			}
			routeDestinations, _, _ := unstructured.NestedSlice(routeMap, "route")
			for _, rd := range routeDestinations {
				rdMap, ok := rd.(map[string]any)
				if !ok {
					continue
				}
				destHost, _, _ := unstructured.NestedString(rdMap, "destination", "host")
				if destHost != "" {
					addVSToSvcEdge("tcp", destHost)
				}
			}
		}

		// Also handle TLS route destinations
		tlsRoutes, _, _ := unstructured.NestedSlice(spec, "tls")
		for _, tlsRoute := range tlsRoutes {
			routeMap, ok := tlsRoute.(map[string]any)
			if !ok {
				continue
			}
			routeDestinations, _, _ := unstructured.NestedSlice(routeMap, "route")
			for _, rd := range routeDestinations {
				rdMap, ok := rd.(map[string]any)
				if !ok {
					continue
				}
				destHost, _, _ := unstructured.NestedString(rdMap, "destination", "host")
				if destHost != "" {
					addVSToSvcEdge("tls", destHost)
				}
			}
		}
	}

	// 14c. Create Istio DestinationRule → Service edges (via spec.host)
	for _, dr := range destinationRuleResources {
		drNs := dr.GetNamespace()
		drName := dr.GetName()
		drID := destinationRuleIDs[drNs+"/"+drName]

		host, _, _ := unstructured.NestedString(dr.Object, "spec", "host")
		if host == "" {
			continue
		}

		svcName, svcNs := parseIstioHost(host, drNs)
		if svcID, ok := serviceIDs[svcNs+"/"+svcName]; ok {
			edges = append(edges, Edge{
				ID:     fmt.Sprintf("%s-to-%s", drID, svcID),
				Source: drID,
				Target: svcID,
				Type:   EdgeConfigures,
			})
		}
	}

	// 14d. Create KNative Serving edges

	// Owner-ref edges: KnativeService → Configuration, KnativeService → Route, etc.

	// Configuration ownerRef → KnativeService (reuse resources from phase 1)
	for _, kcfg := range knativeConfigResources {
		ns := kcfg.GetNamespace()
		name := kcfg.GetName()
		kcfgID := knativeConfigIDs[ns+"/"+name]
		if kcfgID == "" {
			continue
		}
		for _, ref := range kcfg.GetOwnerReferences() {
			if ref.Kind == "Service" && strings.Contains(ref.APIVersion, "serving.knative.dev") {
				if ownerID, ok := knativeServiceIDs[ns+"/"+ref.Name]; ok {
					edges = append(edges, Edge{
						ID:     fmt.Sprintf("%s-to-%s", ownerID, kcfgID),
						Source: ownerID,
						Target: kcfgID,
						Type:   EdgeManages,
					})
				}
			}
		}
	}
	// Route ownerRef → KnativeService
	for _, kroute := range knativeRouteResources {
		ns := kroute.GetNamespace()
		name := kroute.GetName()
		krouteID := knativeRouteIDs[ns+"/"+name]
		if krouteID == "" {
			continue
		}
		for _, ref := range kroute.GetOwnerReferences() {
			if ref.Kind == "Service" && strings.Contains(ref.APIVersion, "serving.knative.dev") {
				if ownerID, ok := knativeServiceIDs[ns+"/"+ref.Name]; ok {
					edges = append(edges, Edge{
						ID:     fmt.Sprintf("%s-to-%s", ownerID, krouteID),
						Source: ownerID,
						Target: krouteID,
						Type:   EdgeManages,
					})
				}
			}
		}
	}
	// Revision ownerRef → Configuration (reuse resources from phase 1)
	for _, krev := range knativeRevisionResources {
		ns := krev.GetNamespace()
		name := krev.GetName()
		krevID := knativeRevisionIDs[ns+"/"+name]
		if krevID == "" {
			continue
		}
		for _, ref := range krev.GetOwnerReferences() {
			if ref.Kind == "Configuration" {
				if ownerID, ok := knativeConfigIDs[ns+"/"+ref.Name]; ok {
					edges = append(edges, Edge{
						ID:     fmt.Sprintf("%s-to-%s", ownerID, krevID),
						Source: ownerID,
						Target: krevID,
						Type:   EdgeManages,
					})
				}
			}
		}
	}
	// Deployment ownerRef → Revision (KNative Revision owns the K8s Deployment)
	for _, deploy := range deployments {
		if !opts.MatchesNamespaceFilter(deploy.Namespace) {
			continue
		}
		for _, ref := range deploy.OwnerReferences {
			if ref.Kind == "Revision" && strings.Contains(ref.APIVersion, "serving.knative.dev") {
				deployID := deploymentIDs[deploy.Namespace+"/"+deploy.Name]
				if krevID, ok := knativeRevisionIDs[deploy.Namespace+"/"+ref.Name]; ok && deployID != "" {
					edges = append(edges, Edge{
						ID:     fmt.Sprintf("%s-to-%s", krevID, deployID),
						Source: krevID,
						Target: deployID,
						Type:   EdgeManages,
					})
				}
			}
		}
	}

	// KnativeRoute → KnativeRevision (EdgeExposes, via status.traffic[].revisionName)
	// Note: spec.traffic may use configurationName instead of revisionName;
	// status.traffic always has the resolved revisionName.
	for _, kroute := range knativeRouteResources {
		krouteNs := kroute.GetNamespace()
		krouteName := kroute.GetName()
		krouteID := knativeRouteIDs[krouteNs+"/"+krouteName]

		trafficTargets, _, _ := unstructured.NestedSlice(kroute.Object, "status", "traffic")
		for _, tt := range trafficTargets {
			ttMap, ok := tt.(map[string]any)
			if !ok {
				continue
			}
			revName, _, _ := unstructured.NestedString(ttMap, "revisionName")
			if revName == "" {
				continue
			}
			if krevID, ok := knativeRevisionIDs[krouteNs+"/"+revName]; ok {
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", krouteID, krevID),
					Source: krouteID,
					Target: krevID,
					Type:   EdgeExposes,
				})
			}
		}
	}

	// KnativeService → K8s Services (EdgeExposes)
	// Connects to: same-name service (ExternalName DNS alias) + per-revision private services (actual pod routing)
	for _, ksvc := range knativeServiceResources {
		ksvcNs := ksvc.GetNamespace()
		ksvcName := ksvc.GetName()
		ksvcID := knativeServiceIDs[ksvcNs+"/"+ksvcName]

		// Same-name service (ExternalName)
		if svcID, ok := serviceIDs[ksvcNs+"/"+ksvcName]; ok {
			edges = append(edges, Edge{
				ID:     fmt.Sprintf("%s-to-%s", ksvcID, svcID),
				Source: ksvcID,
				Target: svcID,
				Type:   EdgeExposes,
			})
		}

		// Per-revision private services (from status.traffic)
		trafficTargets, _, _ := unstructured.NestedSlice(ksvc.Object, "status", "traffic")
		for _, tt := range trafficTargets {
			ttMap, ok := tt.(map[string]any)
			if !ok {
				continue
			}
			revName, _, _ := unstructured.NestedString(ttMap, "revisionName")
			if revName == "" {
				continue
			}
			privateSvcKey := ksvcNs + "/" + revName + "-private"
			if svcID, ok := serviceIDs[privateSvcKey]; ok {
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", ksvcID, svcID),
					Source: ksvcID,
					Target: svcID,
					Type:   EdgeExposes,
				})
			}
		}
		// Fallback: use latestReadyRevisionName
		if len(trafficTargets) == 0 {
			latestRev, _, _ := unstructured.NestedString(ksvc.Object, "status", "latestReadyRevisionName")
			if latestRev != "" {
				privateSvcKey := ksvcNs + "/" + latestRev + "-private"
				if svcID, ok := serviceIDs[privateSvcKey]; ok {
					edges = append(edges, Edge{
						ID:     fmt.Sprintf("%s-to-%s", ksvcID, svcID),
						Source: ksvcID,
						Target: svcID,
						Type:   EdgeExposes,
					})
				}
			}
		}
	}

	// 14e. Create KNative Eventing edges
	// Broker → Trigger (EdgeExposes — Broker routes events to Triggers)
	// Trigger → subscriber target (EdgeExposes, via spec.subscriber.ref)
	for _, trigger := range knativeTriggerResources {
		triggerNs := trigger.GetNamespace()
		triggerName := trigger.GetName()
		triggerID := knativeTriggerIDs[triggerNs+"/"+triggerName]

		// Broker → Trigger (data flow: Broker dispatches events to matching Triggers)
		brokerName, _, _ := unstructured.NestedString(trigger.Object, "spec", "broker")
		if brokerName != "" {
			if brokerID, ok := knativeBrokerIDs[triggerNs+"/"+brokerName]; ok {
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", brokerID, triggerID),
					Source: brokerID,
					Target: triggerID,
					Type:   EdgeExposes,
				})
			}
		}

		// Trigger → subscriber target
		subRefKind, _, _ := unstructured.NestedString(trigger.Object, "spec", "subscriber", "ref", "kind")
		subRefName, _, _ := unstructured.NestedString(trigger.Object, "spec", "subscriber", "ref", "name")
		subRefNs, _, _ := unstructured.NestedString(trigger.Object, "spec", "subscriber", "ref", "namespace")
		if subRefNs == "" {
			subRefNs = triggerNs
		}
		if subRefKind != "" && subRefName != "" {
			targetID := resolveKnativeRef(subRefKind, subRefNs, subRefName, serviceIDs, knativeServiceIDs, knativeBrokerIDs, knativeChannelIDs)
			if targetID != "" {
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-sub-to-%s", triggerID, targetID),
					Source: triggerID,
					Target: targetID,
					Type:   EdgeExposes,
				})
			}
		}
	}

	// Sources → sink target (EdgeExposes, via spec.sink.ref)
	for _, src := range knativeSourceResources {
		srcDef := knativeSourceKinds[src]
		srcNs := src.GetNamespace()
		srcName := src.GetName()
		srcID := fmt.Sprintf("%s/%s/%s", srcDef.prefix, srcNs, srcName)

		sinkRefKind, _, _ := unstructured.NestedString(src.Object, "spec", "sink", "ref", "kind")
		sinkRefName, _, _ := unstructured.NestedString(src.Object, "spec", "sink", "ref", "name")
		sinkRefNs, _, _ := unstructured.NestedString(src.Object, "spec", "sink", "ref", "namespace")
		if sinkRefNs == "" {
			sinkRefNs = srcNs
		}
		if sinkRefKind != "" && sinkRefName != "" {
			targetID := resolveKnativeRef(sinkRefKind, sinkRefNs, sinkRefName, serviceIDs, knativeServiceIDs, knativeBrokerIDs, knativeChannelIDs)
			if targetID != "" {
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-sink-to-%s", srcID, targetID),
					Source: srcID,
					Target: targetID,
					Type:   EdgeExposes,
				})
			}
		}
	}

	// 15. Create Gateway API edges (Gateway → Route, Route → Service)
	for i, route := range gatewayRouteResources {
		ns := route.GetNamespace()
		name := route.GetName()
		routeKind := gatewayRouteKinds[i]
		routeID := routeIDs[routeKind+"/"+ns+"/"+name]

		// Gateway → Route edges (read parentRefs)
		parentRefs, _, _ := unstructured.NestedSlice(route.Object, "spec", "parentRefs")
		for _, pRef := range parentRefs {
			pMap, ok := pRef.(map[string]any)
			if !ok {
				continue
			}
			parentName, _ := pMap["name"].(string)
			parentNS, _ := pMap["namespace"].(string)
			if parentNS == "" {
				parentNS = ns // Default to route's namespace
			}
			gwID := gatewayIDs[parentNS+"/"+parentName]
			if gwID != "" {
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", gwID, routeID),
					Source: gwID,
					Target: routeID,
					Type:   EdgeRoutesTo,
				})
			}
		}

		// Route → Service edges (read backendRefs from rules)
		rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "rules")
		for _, rule := range rules {
			ruleMap, ok := rule.(map[string]any)
			if !ok {
				continue
			}
			backendRefs, _, _ := unstructured.NestedSlice(ruleMap, "backendRefs")
			for _, bRef := range backendRefs {
				bMap, ok := bRef.(map[string]any)
				if !ok {
					continue
				}
				backendName, _ := bMap["name"].(string)
				backendNS, _ := bMap["namespace"].(string)
				if backendNS == "" {
					backendNS = ns // Default to route's namespace
				}
				// Default kind is Service if not specified
				backendKind, _ := bMap["kind"].(string)
				if backendKind == "" || backendKind == "Service" {
					svcKey := backendNS + "/" + backendName
					if svcID, ok := serviceIDs[svcKey]; ok {
						edges = append(edges, Edge{
							ID:     fmt.Sprintf("%s-to-%s", routeID, svcID),
							Source: routeID,
							Target: svcID,
							Type:   EdgeRoutesTo,
						})
					}
				}
			}
		}
	}

	// 15b. Create cert-manager Certificate → Secret edges (via spec.secretName)
	secretIDs := make(map[string]bool)
	for _, node := range nodes {
		if node.Kind == KindSecret {
			secretIDs[node.ID] = true
		}
	}
	for _, cert := range certificateResources {
		ns := cert.GetNamespace()
		certID := fmt.Sprintf("certificate/%s/%s", ns, cert.GetName())
		secretName, _, _ := unstructured.NestedString(cert.Object, "spec", "secretName")
		if secretName != "" {
			secretID := fmt.Sprintf("secret/%s/%s", ns, secretName)
			if secretIDs[secretID] {
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", certID, secretID),
					Source: certID,
					Target: secretID,
					Type:   EdgeManages,
				})
			}
		}
	}

	// 15c. Create cert-manager Certificate → Issuer/ClusterIssuer edges (via spec.issuerRef)
	// Build a lookup of existing node IDs for matching
	existingNodeIDs := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		existingNodeIDs[node.ID] = true
	}
	for _, cert := range certificateResources {
		ns := cert.GetNamespace()
		certID := fmt.Sprintf("certificate/%s/%s", ns, cert.GetName())

		issuerKind, _, _ := unstructured.NestedString(cert.Object, "spec", "issuerRef", "kind")
		issuerName, _, _ := unstructured.NestedString(cert.Object, "spec", "issuerRef", "name")
		if issuerKind == "" || issuerName == "" {
			continue
		}

		var issuerID string
		switch issuerKind {
		case "ClusterIssuer":
			issuerID = fmt.Sprintf("clusterissuer//%s", issuerName)
		case "Issuer":
			issuerID = fmt.Sprintf("issuer/%s/%s", ns, issuerName)
		}
		if issuerID != "" && existingNodeIDs[issuerID] {
			edges = append(edges, Edge{
				ID:     fmt.Sprintf("%s-to-%s", certID, issuerID),
				Source: certID,
				Target: issuerID,
				Type:   EdgeUses,
			})
		}
	}

	// Build Certificate lookup by secretName (for IngressRoute → Certificate edges)
	certBySecret := make(map[string]string) // "ns/secretName" → certificateID
	for _, cert := range certificateResources {
		ns := cert.GetNamespace()
		secretName, _, _ := unstructured.NestedString(cert.Object, "spec", "secretName")
		if secretName != "" {
			certBySecret[ns+"/"+secretName] = fmt.Sprintf("certificate/%s/%s", ns, cert.GetName())
		}
	}

	// 15d. Create Traefik edges
	// IngressRoute/TCP/UDP → Service/TraefikService (EdgeExposes)
	// IngressRoute/TCP → Middleware/MiddlewareTCP (EdgeConfigures)
	// IngressRoute/TCP → TLSOption, TLSStore, ServersTransport/TCP (EdgeConfigures)
	// TraefikService → Service/TraefikService (EdgeExposes)
	// Middleware → Middleware chain (EdgeConfigures)
	traefikEdgeSeen := make(map[string]bool) // dedup: sourceID|targetID

	for _, res := range traefikRouteResources {
		def := traefikRouteKinds[res]
		routeNs := res.GetNamespace()
		routeID := traefikRouteIDs[def.prefix+":"+routeNs+"/"+res.GetName()]
		if routeID == "" {
			continue
		}

		routes, _, _ := unstructured.NestedSlice(res.Object, "spec", "routes")
		for _, route := range routes {
			routeMap, ok := route.(map[string]any)
			if !ok {
				continue
			}

			// Route → Service/TraefikService edges
			// When a serversTransport is present: IngressRoute → Transport → Service
			// Otherwise: IngressRoute → Service (direct)
			svcs, _, _ := unstructured.NestedSlice(routeMap, "services")
			for _, svc := range svcs {
				svcMap, ok := svc.(map[string]any)
				if !ok {
					continue
				}
				svcName, _ := svcMap["name"].(string)
				if svcName == "" {
					continue
				}
				svcNs, _ := svcMap["namespace"].(string)
				if svcNs == "" {
					svcNs = routeNs
				}
				svcKind, _ := svcMap["kind"].(string)

				var targetID string
				if svcKind == "TraefikService" {
					targetID = traefikServiceIDs[svcNs+"/"+svcName]
				} else {
					// Default kind is K8s Service
					targetID = serviceIDs[svcNs+"/"+svcName]
				}
				if targetID == "" {
					continue
				}

				// Check for ServersTransport reference
				stName, _ := svcMap["serversTransport"].(string)
				var stID string
				if stName != "" {
					stPrefix := "serverstransport"
					if def.kind == "IngressRouteTCP" {
						stPrefix = "serverstransporttcp"
					}
					// ServersTransport is resolved relative to the IngressRoute's namespace, not the service's
					stID = traefikConfigIDs[stPrefix+":"+routeNs+"/"+stName]
				}

				if stID != "" {
					// Chain: IngressRoute → ServersTransport → Service
					dedupeKey := routeID + "|" + stID
					if !traefikEdgeSeen[dedupeKey] {
						traefikEdgeSeen[dedupeKey] = true
						edges = append(edges, Edge{
							ID:     fmt.Sprintf("%s-to-%s", routeID, stID),
							Source: routeID,
							Target: stID,
							Type:   EdgeConfigures,
						})
					}
					dedupeKey2 := stID + "|" + targetID
					if !traefikEdgeSeen[dedupeKey2] {
						traefikEdgeSeen[dedupeKey2] = true
						edges = append(edges, Edge{
							ID:     fmt.Sprintf("%s-to-%s", stID, targetID),
							Source: stID,
							Target: targetID,
							Type:   EdgeConfigures,
						})
					}
				} else {
					// Direct: IngressRoute → Service
					dedupeKey := routeID + "|" + targetID
					if traefikEdgeSeen[dedupeKey] {
						continue
					}
					traefikEdgeSeen[dedupeKey] = true
					edges = append(edges, Edge{
						ID:     fmt.Sprintf("%s-to-%s", routeID, targetID),
						Source: routeID,
						Target: targetID,
						Type:   EdgeExposes,
					})
				}
			}

			// Route → Middleware/MiddlewareTCP edges
			middlewares, _, _ := unstructured.NestedSlice(routeMap, "middlewares")
			for _, mw := range middlewares {
				mwMap, ok := mw.(map[string]any)
				if !ok {
					continue
				}
				mwName, _ := mwMap["name"].(string)
				if mwName == "" {
					continue
				}
				mwNs, _ := mwMap["namespace"].(string)
				if mwNs == "" {
					mwNs = routeNs
				}
				// IngressRouteTCP uses MiddlewareTCP, others use Middleware
				mwPrefix := "middleware"
				if def.kind == "IngressRouteTCP" {
					mwPrefix = "middlewaretcp"
				}
				mwID := middlewareIDs[mwPrefix+":"+mwNs+"/"+mwName]
				if mwID == "" {
					continue
				}
				dedupeKey := routeID + "|" + mwID
				if traefikEdgeSeen[dedupeKey] {
					continue
				}
				traefikEdgeSeen[dedupeKey] = true
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", routeID, mwID),
					Source: routeID,
					Target: mwID,
					Type:   EdgeConfigures,
				})
			}
		}

		// TLS-related edges (IngressRoute and IngressRouteTCP only, not UDP)
		if def.kind != "IngressRouteUDP" {
			tlsOptName, _, _ := unstructured.NestedString(res.Object, "spec", "tls", "options", "name")
			if tlsOptName != "" {
				tlsOptNs, _, _ := unstructured.NestedString(res.Object, "spec", "tls", "options", "namespace")
				if tlsOptNs == "" {
					tlsOptNs = routeNs
				}
				tlsOptID := traefikConfigIDs["tlsoption:"+tlsOptNs+"/"+tlsOptName]
				if tlsOptID != "" {
					dedupeKey := routeID + "|" + tlsOptID
					if !traefikEdgeSeen[dedupeKey] {
						traefikEdgeSeen[dedupeKey] = true
						edges = append(edges, Edge{
							ID:     fmt.Sprintf("%s-to-%s", routeID, tlsOptID),
							Source: routeID,
							Target: tlsOptID,
							Type:   EdgeConfigures,
						})
					}
				}
			}

			tlsStoreName, _, _ := unstructured.NestedString(res.Object, "spec", "tls", "store", "name")
			if tlsStoreName != "" {
				tlsStoreNs, _, _ := unstructured.NestedString(res.Object, "spec", "tls", "store", "namespace")
				if tlsStoreNs == "" {
					tlsStoreNs = routeNs
				}
				tlsStoreID := traefikConfigIDs["tlsstore:"+tlsStoreNs+"/"+tlsStoreName]
				if tlsStoreID != "" {
					dedupeKey := routeID + "|" + tlsStoreID
					if !traefikEdgeSeen[dedupeKey] {
						traefikEdgeSeen[dedupeKey] = true
						edges = append(edges, Edge{
							ID:     fmt.Sprintf("%s-to-%s", routeID, tlsStoreID),
							Source: routeID,
							Target: tlsStoreID,
							Type:   EdgeConfigures,
						})
					}
				}
			}

			// IngressRoute → Certificate (via spec.tls.secretName matching cert-manager Certificate)
			tlsSecretName, _, _ := unstructured.NestedString(res.Object, "spec", "tls", "secretName")
			if tlsSecretName != "" {
				certID := certBySecret[routeNs+"/"+tlsSecretName]
				if certID != "" {
					dedupeKey := routeID + "|" + certID
					if !traefikEdgeSeen[dedupeKey] {
						traefikEdgeSeen[dedupeKey] = true
						edges = append(edges, Edge{
							ID:     fmt.Sprintf("%s-to-%s", routeID, certID),
							Source: routeID,
							Target: certID,
							Type:   EdgeConfigures,
						})
					}
				}
			}
		}
	}

	// TraefikService → Service/TraefikService edges
	for _, ts := range traefikServiceResources {
		tsNs := ts.GetNamespace()
		tsID := traefikServiceIDs[tsNs+"/"+ts.GetName()]
		if tsID == "" {
			continue
		}

		spec, _, _ := unstructured.NestedMap(ts.Object, "spec")

		// Collect all service references from weighted, mirroring, and highestRandomWeight
		type svcRef struct {
			name, ns, kind string
		}
		var refs []svcRef

		// Weighted services
		if weighted, ok := spec["weighted"]; ok {
			if wm, ok := weighted.(map[string]any); ok {
				wsvcs, _, _ := unstructured.NestedSlice(wm, "services")
				for _, ws := range wsvcs {
					if wsm, ok := ws.(map[string]any); ok {
						n, _ := wsm["name"].(string)
						ns, _ := wsm["namespace"].(string)
						k, _ := wsm["kind"].(string)
						if n != "" {
							refs = append(refs, svcRef{n, ns, k})
						}
					}
				}
			}
		}

		// HighestRandomWeight services
		if hrw, ok := spec["highestRandomWeight"]; ok {
			if hm, ok := hrw.(map[string]any); ok {
				hsvcs, _, _ := unstructured.NestedSlice(hm, "services")
				for _, hs := range hsvcs {
					if hsm, ok := hs.(map[string]any); ok {
						n, _ := hsm["name"].(string)
						ns, _ := hsm["namespace"].(string)
						k, _ := hsm["kind"].(string)
						if n != "" {
							refs = append(refs, svcRef{n, ns, k})
						}
					}
				}
			}
		}

		// Mirroring: primary + mirrors
		if mirroring, ok := spec["mirroring"]; ok {
			if mm, ok := mirroring.(map[string]any); ok {
				n, _ := mm["name"].(string)
				ns, _ := mm["namespace"].(string)
				k, _ := mm["kind"].(string)
				if n != "" {
					refs = append(refs, svcRef{n, ns, k})
				}
				mirrors, _, _ := unstructured.NestedSlice(mm, "mirrors")
				for _, m := range mirrors {
					if mirrorMap, ok := m.(map[string]any); ok {
						n, _ := mirrorMap["name"].(string)
						ns, _ := mirrorMap["namespace"].(string)
						k, _ := mirrorMap["kind"].(string)
						if n != "" {
							refs = append(refs, svcRef{n, ns, k})
						}
					}
				}
			}
		}

		for _, ref := range refs {
			refNs := ref.ns
			if refNs == "" {
				refNs = tsNs
			}
			var targetID string
			if ref.kind == "TraefikService" {
				targetID = traefikServiceIDs[refNs+"/"+ref.name]
			} else {
				targetID = serviceIDs[refNs+"/"+ref.name]
			}
			if targetID == "" {
				continue
			}
			dedupeKey := tsID + "|" + targetID
			if traefikEdgeSeen[dedupeKey] {
				continue
			}
			traefikEdgeSeen[dedupeKey] = true
			edges = append(edges, Edge{
				ID:     fmt.Sprintf("%s-to-%s", tsID, targetID),
				Source: tsID,
				Target: targetID,
				Type:   EdgeExposes,
			})
		}
	}

	// Middleware → Middleware chain edges (spec.chain.middlewares[])
	for _, mw := range middlewareResources {
		mwNs := mw.GetNamespace()
		mwID := middlewareIDs["middleware:"+mwNs+"/"+mw.GetName()]
		if mwID == "" {
			continue
		}

		chainMWs, _, _ := unstructured.NestedSlice(mw.Object, "spec", "chain", "middlewares")
		for _, chainRef := range chainMWs {
			refMap, ok := chainRef.(map[string]any)
			if !ok {
				continue
			}
			refName, _ := refMap["name"].(string)
			if refName == "" {
				continue
			}
			refNs, _ := refMap["namespace"].(string)
			if refNs == "" {
				refNs = mwNs
			}
			targetID := middlewareIDs["middleware:"+refNs+"/"+refName]
			if targetID == "" {
				continue
			}
			dedupeKey := mwID + "|" + targetID
			if !traefikEdgeSeen[dedupeKey] {
				traefikEdgeSeen[dedupeKey] = true
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", mwID, targetID),
					Source: mwID,
					Target: targetID,
					Type:   EdgeConfigures,
				})
			}
		}
	}

	// ServersTransport → Secret edges (via spec.rootCAsSecrets[] and spec.certificatesSecrets[])
	// TLSOption → Secret edges (via spec.clientAuth.secretNames[])
	// TLSStore → Secret edges (via spec.defaultCertificate.secretName)
	// Creates Secret nodes on-demand since IncludeSecrets may be false
	existingSecretNodes := make(map[string]bool)
	for _, node := range nodes {
		if node.Kind == KindSecret {
			existingSecretNodes[node.ID] = true
		}
	}
	for _, ste := range serversTransportResources {
		st := ste.resource
		stNs := st.GetNamespace()
		stID := traefikConfigIDs[ste.prefix+":"+stNs+"/"+st.GetName()]
		if stID == "" {
			continue
		}

		// Collect secret names from rootCAsSecrets and certificatesSecrets
		var secretNames []string
		rootCAsSecrets, _, _ := unstructured.NestedStringSlice(st.Object, "spec", "rootCAsSecrets")
		secretNames = append(secretNames, rootCAsSecrets...)
		certSecrets, _, _ := unstructured.NestedStringSlice(st.Object, "spec", "certificatesSecrets")
		secretNames = append(secretNames, certSecrets...)

		for _, secretName := range secretNames {
			secretNodeID := fmt.Sprintf("secret/%s/%s", stNs, secretName)

			// Create Secret node if it doesn't already exist
			if !existingSecretNodes[secretNodeID] {
				existingSecretNodes[secretNodeID] = true
				nodes = append(nodes, Node{
					ID:     secretNodeID,
					Kind:   KindSecret,
					Name:   secretName,
					Status: StatusHealthy,
					Data: map[string]any{
						"namespace": stNs,
						"labels":    map[string]string{},
					},
				})
			}

			dedupeKey := stID + "|" + secretNodeID
			if !traefikEdgeSeen[dedupeKey] {
				traefikEdgeSeen[dedupeKey] = true
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", stID, secretNodeID),
					Source: stID,
					Target: secretNodeID,
					Type:   EdgeConfigures,
				})
			}
		}
	}

	// TLSOption → Secret edges (via spec.clientAuth.secretNames[])
	for _, entry := range tlsOptionResources {
		res := entry.resource
		ns := res.GetNamespace()
		optID := traefikConfigIDs["tlsoption:"+ns+"/"+res.GetName()]
		if optID == "" {
			continue
		}
		secretNames, _, _ := unstructured.NestedStringSlice(res.Object, "spec", "clientAuth", "secretNames")
		for _, secretName := range secretNames {
			secretNodeID := fmt.Sprintf("secret/%s/%s", ns, secretName)
			if !existingSecretNodes[secretNodeID] {
				existingSecretNodes[secretNodeID] = true
				nodes = append(nodes, Node{
					ID:     secretNodeID,
					Kind:   KindSecret,
					Name:   secretName,
					Status: StatusHealthy,
					Data: map[string]any{
						"namespace": ns,
						"labels":    map[string]string{},
					},
				})
			}
			dedupeKey := optID + "|" + secretNodeID
			if !traefikEdgeSeen[dedupeKey] {
				traefikEdgeSeen[dedupeKey] = true
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", optID, secretNodeID),
					Source: optID,
					Target: secretNodeID,
					Type:   EdgeConfigures,
				})
			}
		}
	}

	// TLSStore → Secret edges (via spec.defaultCertificate.secretName)
	for _, entry := range tlsStoreResources {
		res := entry.resource
		ns := res.GetNamespace()
		storeID := traefikConfigIDs["tlsstore:"+ns+"/"+res.GetName()]
		if storeID == "" {
			continue
		}
		secretName, _, _ := unstructured.NestedString(res.Object, "spec", "defaultCertificate", "secretName")
		if secretName == "" {
			continue
		}
		secretNodeID := fmt.Sprintf("secret/%s/%s", ns, secretName)
		if !existingSecretNodes[secretNodeID] {
			existingSecretNodes[secretNodeID] = true
			nodes = append(nodes, Node{
				ID:     secretNodeID,
				Kind:   KindSecret,
				Name:   secretName,
				Status: StatusHealthy,
				Data: map[string]any{
					"namespace": ns,
					"labels":    map[string]string{},
				},
			})
		}
		dedupeKey := storeID + "|" + secretNodeID
		if !traefikEdgeSeen[dedupeKey] {
			traefikEdgeSeen[dedupeKey] = true
			edges = append(edges, Edge{
				ID:     fmt.Sprintf("%s-to-%s", storeID, secretNodeID),
				Source: storeID,
				Target: secretNodeID,
				Type:   EdgeConfigures,
			})
		}
	}

	// 15e. Create Contour HTTPProxy edges
	// HTTPProxy → Service (EdgeExposes, via spec.routes[].services[])
	// HTTPProxy → HTTPProxy (EdgeExposes, via spec.includes[])
	// HTTPProxy → Secret (EdgeConfigures, via spec.virtualhost.tls.secretName)
	// HTTPProxy → Service via tcpproxy (EdgeExposes, via spec.tcpproxy.services[])
	contourEdgeSeen := make(map[string]bool) // dedup: sourceID|targetID

	for _, res := range httpProxyResources {
		resNs := res.GetNamespace()
		resID := httpProxyIDs[resNs+"/"+res.GetName()]
		if resID == "" {
			continue
		}

		// HTTPProxy → Service edges (via spec.routes[].services[])
		routes, _, _ := unstructured.NestedSlice(res.Object, "spec", "routes")
		for _, route := range routes {
			routeMap, ok := route.(map[string]any)
			if !ok {
				continue
			}
			svcs, _, _ := unstructured.NestedSlice(routeMap, "services")
			for _, svc := range svcs {
				svcMap, ok := svc.(map[string]any)
				if !ok {
					continue
				}
				svcName, _ := svcMap["name"].(string)
				if svcName == "" {
					continue
				}
				svcNs := resNs
				targetID := serviceIDs[svcNs+"/"+svcName]
				if targetID == "" {
					continue
				}
				dedupeKey := resID + "|" + targetID
				if contourEdgeSeen[dedupeKey] {
					continue
				}
				contourEdgeSeen[dedupeKey] = true
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", resID, targetID),
					Source: resID,
					Target: targetID,
					Type:   EdgeExposes,
				})
			}
		}

		// HTTPProxy → HTTPProxy edges (via spec.includes[])
		includes, _, _ := unstructured.NestedSlice(res.Object, "spec", "includes")
		for _, incl := range includes {
			inclMap, ok := incl.(map[string]any)
			if !ok {
				continue
			}
			inclName, _ := inclMap["name"].(string)
			if inclName == "" {
				continue
			}
			inclNs, _ := inclMap["namespace"].(string)
			if inclNs == "" {
				inclNs = resNs
			}
			targetID := httpProxyIDs[inclNs+"/"+inclName]
			if targetID == "" {
				continue
			}
			dedupeKey := resID + "|" + targetID
			if contourEdgeSeen[dedupeKey] {
				continue
			}
			contourEdgeSeen[dedupeKey] = true
			edges = append(edges, Edge{
				ID:     fmt.Sprintf("%s-to-%s", resID, targetID),
				Source: resID,
				Target: targetID,
				Type:   EdgeExposes,
			})
		}

		// HTTPProxy → Secret edges (via spec.virtualhost.tls.secretName)
		tlsSecretName, _, _ := unstructured.NestedString(res.Object, "spec", "virtualhost", "tls", "secretName")
		if tlsSecretName != "" {
			secretNodeID := fmt.Sprintf("secret/%s/%s", resNs, tlsSecretName)
			// Create stub Secret node if it doesn't already exist (same pattern as Traefik)
			if !existingSecretNodes[secretNodeID] {
				existingSecretNodes[secretNodeID] = true
				nodes = append(nodes, Node{
					ID:     secretNodeID,
					Kind:   KindSecret,
					Name:   tlsSecretName,
					Status: StatusHealthy,
					Data: map[string]any{
						"namespace": resNs,
						"labels":    map[string]string{},
					},
				})
			}
			dedupeKey := resID + "|" + secretNodeID
			if !contourEdgeSeen[dedupeKey] {
				contourEdgeSeen[dedupeKey] = true
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", resID, secretNodeID),
					Source: resID,
					Target: secretNodeID,
					Type:   EdgeConfigures,
				})
			}
			// Also check for cert-manager Certificate edge
			certID := certBySecret[resNs+"/"+tlsSecretName]
			if certID != "" {
				dedupeKey2 := resID + "|" + certID
				if !contourEdgeSeen[dedupeKey2] {
					contourEdgeSeen[dedupeKey2] = true
					edges = append(edges, Edge{
						ID:     fmt.Sprintf("%s-to-%s", resID, certID),
						Source: resID,
						Target: certID,
						Type:   EdgeConfigures,
					})
				}
			}
		}

		// HTTPProxy → Service via tcpproxy (via spec.tcpproxy.services[])
		tcpSvcs, _, _ := unstructured.NestedSlice(res.Object, "spec", "tcpproxy", "services")
		for _, svc := range tcpSvcs {
			svcMap, ok := svc.(map[string]any)
			if !ok {
				continue
			}
			svcName, _ := svcMap["name"].(string)
			if svcName == "" {
				continue
			}
			svcNs := resNs
			targetID := serviceIDs[svcNs+"/"+svcName]
			if targetID == "" {
				continue
			}
			dedupeKey := resID + "|" + targetID
			if contourEdgeSeen[dedupeKey] {
				continue
			}
			contourEdgeSeen[dedupeKey] = true
			edges = append(edges, Edge{
				ID:     fmt.Sprintf("%s-to-%s", resID, targetID),
				Source: resID,
				Target: targetID,
				Type:   EdgeExposes,
			})
		}
	}

	// 16. Add generic CRD nodes connected via owner references
	// Only includes CRDs already being watched and with owner refs to existing nodes
	if opts.IncludeGenericCRDs {
		nodes, edges = b.addGenericCRDNodes(nodes, edges, opts)
	}

	// 17. Annotate workload nodes with NetworkPolicy coverage (optional)
	if opts.ShowPolicyEffect {
		annotateNodePolicyCoverage(nodes, edges, netpols, deployments, statefulsets, daemonsets)
	}

	// Summary mode: stamp collapsed pod counts onto their workload nodes.
	stampPodSummaries(nodes, podSummaries)

	topo := &Topology{Nodes: stampAuditKeys(nodes), Edges: edges, Warnings: warnings}

	// Add CRD discovery status
	if b.dynamic != nil {
		topo.CRDDiscoveryStatus = string(b.dynamic.GetDiscoveryStatus())
	}

	return topo, nil
}

// buildTrafficTopology creates a network-focused view
// Shows only nodes that are part of actual traffic paths:
//   - Internet -> Ingress -> Service -> Pod
//   - Internet -> Gateway -> Route -> Service -> Pod
func (b *Builder) buildTrafficTopology(opts BuildOptions) (*Topology, error) {
	nodes := make([]Node, 0)
	edges := make([]Edge, 0)
	warnings := make([]string, 0)

	// First, collect all raw data
	ingresses, ingressErr := b.provider.Ingresses()
	if ingressErr != nil {
		log.Printf("WARNING [topology/traffic] Failed to list Ingresses: %v", ingressErr)
		warnings = append(warnings, fmt.Sprintf("Failed to list Ingresses: %v", ingressErr))
	}
	services, svcErr := b.provider.Services()
	if svcErr != nil {
		log.Printf("WARNING [topology/traffic] Failed to list Services: %v", svcErr)
		warnings = append(warnings, fmt.Sprintf("Failed to list Services: %v", svcErr))
	}
	pods, podErr := b.provider.Pods()
	if podErr != nil {
		log.Printf("WARNING [topology/traffic] Failed to list Pods: %v", podErr)
		warnings = append(warnings, fmt.Sprintf("Failed to list Pods: %v", podErr))
	}

	// Pre-index pods by namespace to avoid O(services × all_pods) complexity
	podsByNS := make(map[string][]*corev1.Pod)
	for _, pod := range pods {
		podsByNS[pod.Namespace] = append(podsByNS[pod.Namespace], pod)
	}

	// Track which services and pods to include
	servicesToInclude := make(map[string]*corev1.Service) // svcKey -> service
	servicesFromIngress := make(map[string]bool)          // svcKey -> has ingress
	serviceIDs := make(map[string]string)                 // svcKey -> svcID

	// Collect Gateway API resources from dynamic cache
	trafficDynamicCache := b.dynamic
	trafficResourceDiscovery := b.dynamic
	var trafficGateways []*unstructured.Unstructured
	var trafficRoutes []*unstructured.Unstructured
	var trafficRouteKinds []string
	if trafficDynamicCache != nil && trafficResourceDiscovery != nil {
		if gwGVR, ok := trafficResourceDiscovery.GetGVR("Gateway"); ok {
			gws, err := trafficDynamicCache.ListNamespaces(gwGVR, opts.Namespaces)
			if err != nil {
				log.Printf("WARNING [topology/traffic] Failed to list Gateways: %v", err)
				warnings = append(warnings, fmt.Sprintf("Failed to list Gateways: %v", err))
			} else {
				trafficGateways = gws
			}
		}
		for _, routeKind := range []string{"HTTPRoute", "GRPCRoute", "TCPRoute", "TLSRoute"} {
			if rGVR, ok := trafficResourceDiscovery.GetGVR(routeKind); ok {
				rts, err := trafficDynamicCache.ListNamespaces(rGVR, opts.Namespaces)
				if err != nil {
					log.Printf("WARNING [topology/traffic] Failed to list %s: %v", routeKind, err)
					warnings = append(warnings, fmt.Sprintf("Failed to list %s: %v", routeKind, err))
				} else {
					for _, rt := range rts {
						trafficRoutes = append(trafficRoutes, rt)
						trafficRouteKinds = append(trafficRouteKinds, routeKind)
					}
				}
			}
		}
	}

	// Collect Istio resources from dynamic cache
	var trafficVirtualServices []*unstructured.Unstructured
	var trafficIstioGateways []*unstructured.Unstructured
	if trafficDynamicCache != nil && trafficResourceDiscovery != nil {
		if vsGVR, ok := trafficResourceDiscovery.GetGVRWithGroup("VirtualService", "networking.istio.io"); ok {
			vss, err := trafficDynamicCache.ListNamespaces(vsGVR, opts.Namespaces)
			if err != nil {
				log.Printf("WARNING [topology/traffic] Failed to list Istio VirtualServices: %v", err)
				warnings = append(warnings, fmt.Sprintf("Failed to list Istio VirtualServices: %v", err))
			} else {
				trafficVirtualServices = vss
			}
		}
		if igwGVR, ok := trafficResourceDiscovery.GetGVRWithGroup("Gateway", "networking.istio.io"); ok {
			igws, err := trafficDynamicCache.ListNamespaces(igwGVR, opts.Namespaces)
			if err != nil {
				log.Printf("WARNING [topology/traffic] Failed to list Istio Gateways: %v", err)
				warnings = append(warnings, fmt.Sprintf("Failed to list Istio Gateways: %v", err))
			} else {
				trafficIstioGateways = igws
			}
		}
	}

	// Collect KNative Serving resources from dynamic cache
	var trafficKnativeServices []*unstructured.Unstructured
	if trafficDynamicCache != nil && trafficResourceDiscovery != nil {
		if ksvcGVR, ok := trafficResourceDiscovery.GetGVRWithGroup("Service", "serving.knative.dev"); ok {
			ksvcs, err := trafficDynamicCache.ListNamespaces(ksvcGVR, opts.Namespaces)
			if err != nil {
				log.Printf("WARNING [topology/traffic] Failed to list KNative Services: %v", err)
				warnings = append(warnings, fmt.Sprintf("Failed to list KNative Services: %v", err))
			} else {
				trafficKnativeServices = ksvcs
			}
		}
	}

	// Step 1: Find services referenced by ingresses
	for _, ing := range ingresses {
		if !opts.MatchesNamespaceFilter(ing.Namespace) {
			continue
		}
		for _, rule := range ing.Spec.Rules {
			if rule.HTTP == nil {
				continue
			}
			for _, path := range rule.HTTP.Paths {
				if path.Backend.Service != nil {
					svcKey := ing.Namespace + "/" + path.Backend.Service.Name
					servicesFromIngress[svcKey] = true
				}
			}
		}
	}

	// Step 1b: Find services referenced by Gateway API routes
	servicesFromGateway := make(map[string]bool)
	for _, route := range trafficRoutes {
		ns := route.GetNamespace()
		if !opts.MatchesNamespaceFilter(ns) {
			continue
		}
		rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "rules")
		for _, rule := range rules {
			ruleMap, ok := rule.(map[string]any)
			if !ok {
				continue
			}
			backendRefs, _, _ := unstructured.NestedSlice(ruleMap, "backendRefs")
			for _, bRef := range backendRefs {
				bMap, ok := bRef.(map[string]any)
				if !ok {
					continue
				}
				backendName, _ := bMap["name"].(string)
				backendNS, _ := bMap["namespace"].(string)
				if backendNS == "" {
					backendNS = ns
				}
				backendKind, _ := bMap["kind"].(string)
				if backendKind == "" || backendKind == "Service" {
					svcKey := backendNS + "/" + backendName
					servicesFromGateway[svcKey] = true
				}
			}
		}
	}

	// Step 1c: Find services referenced by Istio VirtualServices
	servicesFromIstio := make(map[string]bool)
	for _, vs := range trafficVirtualServices {
		vsNs := vs.GetNamespace()
		if !opts.MatchesNamespaceFilter(vsNs) {
			continue
		}
		spec, _, _ := unstructured.NestedMap(vs.Object, "spec")
		if spec == nil {
			continue
		}
		for _, routeKey := range []string{"http", "tcp", "tls"} {
			routes, _, _ := unstructured.NestedSlice(spec, routeKey)
			for _, route := range routes {
				routeMap, ok := route.(map[string]any)
				if !ok {
					continue
				}
				routeDests, _, _ := unstructured.NestedSlice(routeMap, "route")
				for _, rd := range routeDests {
					rdMap, ok := rd.(map[string]any)
					if !ok {
						continue
					}
					destHost, _, _ := unstructured.NestedString(rdMap, "destination", "host")
					if destHost == "" {
						continue
					}
					svcName, svcNs := parseIstioHost(destHost, vsNs)
					svcKey := svcNs + "/" + svcName
					servicesFromIstio[svcKey] = true
				}
			}
		}
	}

	// Step 1d: Find services referenced by KNative Services
	// KNative creates per-revision private services ({revisionName}-private) that actually
	// select pods. The same-name service ({ksvcName}) is ExternalName and has no pod selector.
	servicesFromKnative := make(map[string]bool)
	for _, ksvc := range trafficKnativeServices {
		ns := ksvc.GetNamespace()
		if !opts.MatchesNamespaceFilter(ns) {
			continue
		}
		trafficTargets, _, _ := unstructured.NestedSlice(ksvc.Object, "status", "traffic")
		for _, tt := range trafficTargets {
			ttMap, ok := tt.(map[string]any)
			if !ok {
				continue
			}
			revName, _, _ := unstructured.NestedString(ttMap, "revisionName")
			if revName != "" {
				servicesFromKnative[ns+"/"+revName+"-private"] = true
			}
		}
		// Fallback: if no traffic targets, use latestReadyRevisionName
		if len(trafficTargets) == 0 {
			latestRev, _, _ := unstructured.NestedString(ksvc.Object, "status", "latestReadyRevisionName")
			if latestRev != "" {
				servicesFromKnative[ns+"/"+latestRev+"-private"] = true
			}
		}
	}

	// Collect Traefik IngressRoute resources from dynamic cache
	var trafficTraefikRoutes []*unstructured.Unstructured
	var trafficTraefikRouteKinds []string
	var trafficTraefikServices []*unstructured.Unstructured
	var trafficMiddlewares []*unstructured.Unstructured
	var trafficMiddlewareTCPs []*unstructured.Unstructured
	if trafficDynamicCache != nil && trafficResourceDiscovery != nil {
		for _, routeKind := range []string{"IngressRoute", "IngressRouteTCP", "IngressRouteUDP"} {
			if gvr, ok := trafficResourceDiscovery.GetGVR(routeKind); ok {
				rts, err := trafficDynamicCache.ListNamespaces(gvr, opts.Namespaces)
				if err != nil {
					log.Printf("WARNING [topology/traffic] Failed to list Traefik %s: %v", routeKind, err)
					warnings = append(warnings, fmt.Sprintf("Failed to list Traefik %s: %v", routeKind, err))
				} else {
					for _, rt := range rts {
						trafficTraefikRoutes = append(trafficTraefikRoutes, rt)
						trafficTraefikRouteKinds = append(trafficTraefikRouteKinds, routeKind)
					}
				}
			}
		}
		if tsGVR, ok := trafficResourceDiscovery.GetGVR("TraefikService"); ok {
			tss, err := trafficDynamicCache.ListNamespaces(tsGVR, opts.Namespaces)
			if err != nil {
				log.Printf("WARNING [topology/traffic] Failed to list TraefikServices: %v", err)
				warnings = append(warnings, fmt.Sprintf("Failed to list TraefikServices: %v", err))
			} else {
				trafficTraefikServices = tss
			}
		}
		if mwGVR, ok := trafficResourceDiscovery.GetGVR("Middleware"); ok {
			mws, err := trafficDynamicCache.ListNamespaces(mwGVR, opts.Namespaces)
			if err != nil {
				log.Printf("WARNING [topology/traffic] Failed to list Traefik Middlewares: %v", err)
				warnings = append(warnings, fmt.Sprintf("Failed to list Traefik Middlewares: %v", err))
			} else {
				trafficMiddlewares = mws
			}
		}
		if mtGVR, ok := trafficResourceDiscovery.GetGVR("MiddlewareTCP"); ok {
			mts, err := trafficDynamicCache.ListNamespaces(mtGVR, opts.Namespaces)
			if err != nil {
				log.Printf("WARNING [topology/traffic] Failed to list Traefik MiddlewareTCPs: %v", err)
				warnings = append(warnings, fmt.Sprintf("Failed to list Traefik MiddlewareTCPs: %v", err))
			} else {
				trafficMiddlewareTCPs = mts
			}
		}
	}

	// Step 1e: Find services referenced by Traefik IngressRoutes
	servicesFromTraefik := make(map[string]bool)
	for _, rt := range trafficTraefikRoutes {
		ns := rt.GetNamespace()
		if !opts.MatchesNamespaceFilter(ns) {
			continue
		}
		routes, _, _ := unstructured.NestedSlice(rt.Object, "spec", "routes")
		for _, route := range routes {
			routeMap, ok := route.(map[string]any)
			if !ok {
				continue
			}
			svcs, _, _ := unstructured.NestedSlice(routeMap, "services")
			for _, svc := range svcs {
				svcMap, ok := svc.(map[string]any)
				if !ok {
					continue
				}
				svcName, _ := svcMap["name"].(string)
				svcKind, _ := svcMap["kind"].(string)
				if svcName == "" || (svcKind != "" && svcKind != "Service") {
					continue
				}
				svcNs, _ := svcMap["namespace"].(string)
				if svcNs == "" {
					svcNs = ns
				}
				servicesFromTraefik[svcNs+"/"+svcName] = true
			}
		}
	}
	// Also find services referenced by TraefikService (weighted/mirroring)
	for _, ts := range trafficTraefikServices {
		ns := ts.GetNamespace()
		if !opts.MatchesNamespaceFilter(ns) {
			continue
		}
		spec, _, _ := unstructured.NestedMap(ts.Object, "spec")
		if spec == nil {
			continue
		}
		for _, svcListKey := range []string{"weighted", "highestRandomWeight"} {
			svcList, _, _ := unstructured.NestedSlice(spec, svcListKey, "services")
			for _, svc := range svcList {
				svcMap, ok := svc.(map[string]any)
				if !ok {
					continue
				}
				svcName, _ := svcMap["name"].(string)
				svcKind, _ := svcMap["kind"].(string)
				if svcName == "" || (svcKind != "" && svcKind != "Service") {
					continue
				}
				svcNs, _ := svcMap["namespace"].(string)
				if svcNs == "" {
					svcNs = ns
				}
				servicesFromTraefik[svcNs+"/"+svcName] = true
			}
		}
		// mirroring: primary service + mirror targets
		mirrorBody, _, _ := unstructured.NestedMap(spec, "mirroring")
		if mirrorBody != nil {
			bodyName, _ := mirrorBody["name"].(string)
			bodyKind, _ := mirrorBody["kind"].(string)
			if bodyName != "" && (bodyKind == "" || bodyKind == "Service") {
				bodyNs, _ := mirrorBody["namespace"].(string)
				if bodyNs == "" {
					bodyNs = ns
				}
				servicesFromTraefik[bodyNs+"/"+bodyName] = true
			}
			mirrors, _, _ := unstructured.NestedSlice(mirrorBody, "mirrors")
			for _, m := range mirrors {
				if mm, ok := m.(map[string]any); ok {
					mName, _ := mm["name"].(string)
					mKind, _ := mm["kind"].(string)
					if mName != "" && (mKind == "" || mKind == "Service") {
						mNs, _ := mm["namespace"].(string)
						if mNs == "" {
							mNs = ns
						}
						servicesFromTraefik[mNs+"/"+mName] = true
					}
				}
			}
		}
	}

	// Collect Contour HTTPProxy resources from dynamic cache
	var trafficHTTPProxies []*unstructured.Unstructured
	if trafficDynamicCache != nil && trafficResourceDiscovery != nil {
		if gvr, ok := trafficResourceDiscovery.GetGVR("HTTPProxy"); ok {
			hps, err := trafficDynamicCache.ListNamespaces(gvr, opts.Namespaces)
			if err != nil {
				log.Printf("WARNING [topology/traffic] Failed to list Contour HTTPProxy: %v", err)
				warnings = append(warnings, fmt.Sprintf("Failed to list Contour HTTPProxy: %v", err))
			} else {
				trafficHTTPProxies = hps
			}
		}
	}

	// Step 1f: Find services referenced by Contour HTTPProxy
	servicesFromContour := make(map[string]bool)
	for _, hp := range trafficHTTPProxies {
		ns := hp.GetNamespace()
		if !opts.MatchesNamespaceFilter(ns) {
			continue
		}
		routes, _, _ := unstructured.NestedSlice(hp.Object, "spec", "routes")
		for _, route := range routes {
			routeMap, ok := route.(map[string]any)
			if !ok {
				continue
			}
			svcs, _, _ := unstructured.NestedSlice(routeMap, "services")
			for _, svc := range svcs {
				svcMap, ok := svc.(map[string]any)
				if !ok {
					continue
				}
				svcName, _ := svcMap["name"].(string)
				if svcName == "" {
					continue
				}
				servicesFromContour[ns+"/"+svcName] = true
			}
		}
		// Also check tcpproxy services
		tcpSvcs, _, _ := unstructured.NestedSlice(hp.Object, "spec", "tcpproxy", "services")
		for _, svc := range tcpSvcs {
			svcMap, ok := svc.(map[string]any)
			if !ok {
				continue
			}
			svcName, _ := svcMap["name"].(string)
			if svcName == "" {
				continue
			}
			servicesFromContour[ns+"/"+svcName] = true
		}
	}

	// Step 2: Find all services and check which have pods
	for _, svc := range services {
		if !opts.MatchesNamespaceFilter(svc.Namespace) {
			continue
		}
		svcKey := svc.Namespace + "/" + svc.Name

		// Check if any pod matches this service's selector (using namespace-indexed pods)
		hasPods := false
		for _, pod := range podsByNS[svc.Namespace] {
			if matchesSelector(pod.Labels, svc.Spec.Selector) {
				hasPods = true
				break
			}
		}

		// Include service if: referenced by ingress, gateway route, istio VS, knative, contour, OR has matching pods
		if servicesFromIngress[svcKey] || servicesFromGateway[svcKey] || servicesFromIstio[svcKey] || servicesFromKnative[svcKey] || servicesFromTraefik[svcKey] || servicesFromContour[svcKey] || hasPods {
			servicesToInclude[svcKey] = svc
		}
	}

	// Pre-index included services by namespace for O(pods × services_per_namespace) pod matching
	servicesByNS := make(map[string]map[string]*corev1.Service) // ns -> svcKey -> service
	for svcKey, svc := range servicesToInclude {
		if servicesByNS[svc.Namespace] == nil {
			servicesByNS[svc.Namespace] = make(map[string]*corev1.Service)
		}
		servicesByNS[svc.Namespace][svcKey] = svc
	}

	// Step 3: Build Ingress nodes and edges
	ingressIDs := make([]string, 0)
	for _, ing := range ingresses {
		if !opts.MatchesNamespaceFilter(ing.Namespace) {
			continue
		}

		ingID := fmt.Sprintf("ingress/%s/%s", ing.Namespace, ing.Name)
		ingressIDs = append(ingressIDs, ingID)

		var host string
		if len(ing.Spec.Rules) > 0 && ing.Spec.Rules[0].Host != "" {
			host = ing.Spec.Rules[0].Host
		}

		nodes = append(nodes, Node{
			ID:     ingID,
			Kind:   KindIngress,
			Name:   ing.Name,
			Status: StatusHealthy,
			Data: map[string]any{
				"namespace": ing.Namespace,
				"hostname":  host,
				"tls":       len(ing.Spec.TLS) > 0,
				"labels":    ing.Labels,
			},
		})

		// Connect to backend Services (only if service is included)
		for _, rule := range ing.Spec.Rules {
			if rule.HTTP == nil {
				continue
			}
			for _, path := range rule.HTTP.Paths {
				if path.Backend.Service != nil {
					svcKey := ing.Namespace + "/" + path.Backend.Service.Name
					if _, ok := servicesToInclude[svcKey]; ok {
						svcID := fmt.Sprintf("service/%s/%s", ing.Namespace, path.Backend.Service.Name)
						serviceIDs[svcKey] = svcID
						edges = append(edges, Edge{
							ID:     fmt.Sprintf("%s-to-%s", ingID, svcID),
							Source: ingID,
							Target: svcID,
							Type:   EdgeRoutesTo,
						})
					}
				}
			}
		}
	}

	// Step 3b: Build Gateway and route nodes/edges for traffic view
	trafficGatewayIDs := make([]string, 0)
	trafficGwIDMap := make(map[string]string) // ns/name -> gwID
	for _, gw := range trafficGateways {
		ns := gw.GetNamespace()
		if !opts.MatchesNamespaceFilter(ns) {
			continue
		}
		name := gw.GetName()
		gwID := fmt.Sprintf("gateway/%s/%s", ns, name)
		trafficGatewayIDs = append(trafficGatewayIDs, gwID)
		trafficGwIDMap[ns+"/"+name] = gwID

		listeners, _, _ := unstructured.NestedSlice(gw.Object, "spec", "listeners")
		addresses, _, _ := unstructured.NestedSlice(gw.Object, "status", "addresses")
		var addrList []string
		for _, addr := range addresses {
			if addrMap, ok := addr.(map[string]any); ok {
				if val, ok := addrMap["value"].(string); ok {
					addrList = append(addrList, val)
				}
			}
		}

		nodes = append(nodes, Node{
			ID:     gwID,
			Kind:   KindGateway,
			Name:   name,
			Status: getGatewayHealth(gw),
			Data: map[string]any{
				"namespace":     ns,
				"listenerCount": len(listeners),
				"addresses":     addrList,
				"labels":        gw.GetLabels(),
				"apiVersion":    gw.GetAPIVersion(),
			},
		})
	}

	for i, route := range trafficRoutes {
		ns := route.GetNamespace()
		if !opts.MatchesNamespaceFilter(ns) {
			continue
		}
		name := route.GetName()
		routeKind := trafficRouteKinds[i]
		kindLower := strings.ToLower(routeKind)
		routeID := fmt.Sprintf("%s/%s/%s", kindLower, ns, name)

		hostnames, _, _ := unstructured.NestedStringSlice(route.Object, "spec", "hostnames")
		rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "rules")

		nodes = append(nodes, Node{
			ID:     routeID,
			Kind:   NodeKind(routeKind),
			Name:   name,
			Status: getRouteHealth(route),
			Data: map[string]any{
				"namespace":  ns,
				"hostnames":  hostnames,
				"rulesCount": len(rules),
				"labels":     route.GetLabels(),
				"apiVersion": route.GetAPIVersion(),
			},
		})

		// Gateway → Route edges
		parentRefs, _, _ := unstructured.NestedSlice(route.Object, "spec", "parentRefs")
		for _, pRef := range parentRefs {
			pMap, ok := pRef.(map[string]any)
			if !ok {
				continue
			}
			parentName, _ := pMap["name"].(string)
			parentNS, _ := pMap["namespace"].(string)
			if parentNS == "" {
				parentNS = ns
			}
			if gwID, ok := trafficGwIDMap[parentNS+"/"+parentName]; ok {
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", gwID, routeID),
					Source: gwID,
					Target: routeID,
					Type:   EdgeRoutesTo,
				})
			}
		}

		// Route → Service edges
		for _, rule := range rules {
			ruleMap, ok := rule.(map[string]any)
			if !ok {
				continue
			}
			backendRefs, _, _ := unstructured.NestedSlice(ruleMap, "backendRefs")
			for _, bRef := range backendRefs {
				bMap, ok := bRef.(map[string]any)
				if !ok {
					continue
				}
				backendName, _ := bMap["name"].(string)
				backendNS, _ := bMap["namespace"].(string)
				if backendNS == "" {
					backendNS = ns
				}
				backendKind, _ := bMap["kind"].(string)
				if backendKind == "" || backendKind == "Service" {
					svcKey := backendNS + "/" + backendName
					if _, ok := servicesToInclude[svcKey]; ok {
						svcID := fmt.Sprintf("service/%s/%s", backendNS, backendName)
						serviceIDs[svcKey] = svcID
						edges = append(edges, Edge{
							ID:     fmt.Sprintf("%s-to-%s", routeID, svcID),
							Source: routeID,
							Target: svcID,
							Type:   EdgeRoutesTo,
						})
					}
				}
			}
		}
	}

	// Step 3c: Build Istio VirtualService + IstioGateway nodes/edges for traffic view
	trafficIstioGatewayIDs := make([]string, 0)
	trafficIstioGwIDMap := make(map[string]string) // ns/name -> igwID
	trafficVsIDMap := make(map[string]string)      // ns/name -> vsID
	trafficVsToSvcSeen := make(map[string]bool)    // dedup VS→Service edges

	for _, igw := range trafficIstioGateways {
		ns := igw.GetNamespace()
		if !opts.MatchesNamespaceFilter(ns) {
			continue
		}
		name := igw.GetName()
		igwID := fmt.Sprintf("istiogateway/%s/%s", ns, name)
		trafficIstioGatewayIDs = append(trafficIstioGatewayIDs, igwID)
		trafficIstioGwIDMap[ns+"/"+name] = igwID

		servers, _, _ := unstructured.NestedSlice(igw.Object, "spec", "servers")
		selector, _, _ := unstructured.NestedStringMap(igw.Object, "spec", "selector")

		nodeStatus := StatusHealthy
		if len(servers) == 0 {
			nodeStatus = StatusUnhealthy
		}

		nodes = append(nodes, Node{
			ID:     igwID,
			Kind:   KindIstioGateway,
			Name:   name,
			Status: nodeStatus,
			Data: map[string]any{
				"namespace":   ns,
				"serverCount": len(servers),
				"selector":    selector,
				"labels":      igw.GetLabels(),
				"apiVersion":  igw.GetAPIVersion(),
			},
		})
	}

	for _, vs := range trafficVirtualServices {
		vsNs := vs.GetNamespace()
		if !opts.MatchesNamespaceFilter(vsNs) {
			continue
		}
		vsName := vs.GetName()
		vsID := fmt.Sprintf("virtualservice/%s/%s", vsNs, vsName)
		trafficVsIDMap[vsNs+"/"+vsName] = vsID

		spec, _, _ := unstructured.NestedMap(vs.Object, "spec")
		if spec == nil {
			continue
		}

		hosts, _, _ := unstructured.NestedStringSlice(spec, "hosts")
		gateways, _, _ := unstructured.NestedStringSlice(spec, "gateways")
		httpRoutes, _, _ := unstructured.NestedSlice(spec, "http")
		tcpRoutes, _, _ := unstructured.NestedSlice(spec, "tcp")
		tlsRoutes, _, _ := unstructured.NestedSlice(spec, "tls")
		routeCount := len(httpRoutes) + len(tcpRoutes) + len(tlsRoutes)

		nodeStatus := StatusHealthy
		if routeCount == 0 {
			nodeStatus = StatusUnhealthy
		}

		nodes = append(nodes, Node{
			ID:     vsID,
			Kind:   KindVirtualService,
			Name:   vsName,
			Status: nodeStatus,
			Data: map[string]any{
				"namespace":  vsNs,
				"hosts":      hosts,
				"gateways":   gateways,
				"routeCount": routeCount,
				"labels":     vs.GetLabels(),
				"apiVersion": vs.GetAPIVersion(),
			},
		})

		// IstioGateway → VirtualService edges
		for _, gwRef := range gateways {
			if gwRef == "mesh" {
				continue
			}
			gwNs := vsNs
			gwName := gwRef
			if parts := strings.SplitN(gwRef, "/", 2); len(parts) == 2 {
				gwNs = parts[0]
				gwName = parts[1]
			}
			if igwID, ok := trafficIstioGwIDMap[gwNs+"/"+gwName]; ok {
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", igwID, vsID),
					Source: igwID,
					Target: vsID,
					Type:   EdgeExposes,
				})
			}
		}

		// VirtualService → Service edges (deduped)
		for _, routeKey := range []string{"http", "tcp", "tls"} {
			routes, _, _ := unstructured.NestedSlice(spec, routeKey)
			for _, route := range routes {
				routeMap, ok := route.(map[string]any)
				if !ok {
					continue
				}
				routeDests, _, _ := unstructured.NestedSlice(routeMap, "route")
				for _, rd := range routeDests {
					rdMap, ok := rd.(map[string]any)
					if !ok {
						continue
					}
					destHost, _, _ := unstructured.NestedString(rdMap, "destination", "host")
					if destHost == "" {
						continue
					}
					svcName, svcNs := parseIstioHost(destHost, vsNs)
					svcKey := svcNs + "/" + svcName
					if _, ok := servicesToInclude[svcKey]; !ok {
						continue
					}
					svcID := fmt.Sprintf("service/%s/%s", svcNs, svcName)
					serviceIDs[svcKey] = svcID
					dedupeKey := vsID + "|" + svcID
					if trafficVsToSvcSeen[dedupeKey] {
						continue
					}
					trafficVsToSvcSeen[dedupeKey] = true
					edges = append(edges, Edge{
						ID:     fmt.Sprintf("%s-to-%s", vsID, svcID),
						Source: vsID,
						Target: svcID,
						Type:   EdgeExposes,
					})
				}
			}
		}
	}

	// Step 3d: Build KNative Serving nodes/edges for traffic view
	// KNative traffic flow: Internet → KnativeService → K8s Service → Pods
	// KnativeRoute shown as subtitle data on KnativeService (URL comes from Route)
	trafficKnativeServiceIDs := make([]string, 0)
	for _, ksvc := range trafficKnativeServices {
		ns := ksvc.GetNamespace()
		if !opts.MatchesNamespaceFilter(ns) {
			continue
		}
		name := ksvc.GetName()
		ksvcID := fmt.Sprintf("knativeservice/%s/%s", ns, name)
		trafficKnativeServiceIDs = append(trafficKnativeServiceIDs, ksvcID)

		// Get URL from status (set by KNative Route)
		url, _, _ := unstructured.NestedString(ksvc.Object, "status", "url")
		latestRevision, _, _ := unstructured.NestedString(ksvc.Object, "status", "latestReadyRevisionName")

		// Get traffic splits from status
		trafficTargets, _, _ := unstructured.NestedSlice(ksvc.Object, "status", "traffic")
		var trafficSummary string
		for _, tt := range trafficTargets {
			ttMap, ok := tt.(map[string]any)
			if !ok {
				continue
			}
			percent, _, _ := unstructured.NestedInt64(ttMap, "percent")
			revName, _, _ := unstructured.NestedString(ttMap, "revisionName")
			if trafficSummary != "" {
				trafficSummary += ", "
			}
			if revName != "" {
				trafficSummary += fmt.Sprintf("%d%% → %s", percent, revName)
			}
		}

		nodes = append(nodes, Node{
			ID:     ksvcID,
			Kind:   KindKnativeService,
			Name:   name,
			Status: extractGenericStatus(ksvc),
			Data: map[string]any{
				"namespace":      ns,
				"url":            url,
				"latestRevision": latestRevision,
				"trafficSummary": trafficSummary,
				"labels":         ksvc.GetLabels(),
				"apiVersion":     ksvc.GetAPIVersion(),
			},
		})

		// KnativeService → revision private services (the ones that actually select pods)
		for _, tt := range trafficTargets {
			ttMap, ok := tt.(map[string]any)
			if !ok {
				continue
			}
			revName, _, _ := unstructured.NestedString(ttMap, "revisionName")
			if revName == "" {
				continue
			}
			privateSvcKey := ns + "/" + revName + "-private"
			if _, ok := servicesToInclude[privateSvcKey]; ok {
				svcID := fmt.Sprintf("service/%s/%s", ns, revName+"-private")
				serviceIDs[privateSvcKey] = svcID
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", ksvcID, svcID),
					Source: ksvcID,
					Target: svcID,
					Type:   EdgeExposes,
				})
			}
		}
		// Fallback for no traffic targets
		if len(trafficTargets) == 0 && latestRevision != "" {
			privateSvcKey := ns + "/" + latestRevision + "-private"
			if _, ok := servicesToInclude[privateSvcKey]; ok {
				svcID := fmt.Sprintf("service/%s/%s", ns, latestRevision+"-private")
				serviceIDs[privateSvcKey] = svcID
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", ksvcID, svcID),
					Source: ksvcID,
					Target: svcID,
					Type:   EdgeExposes,
				})
			}
		}
	}

	// Step 3e: Build Traefik IngressRoute nodes/edges for traffic view
	// Traffic flow: Internet → IngressRoute → (TraefikService →) Service → Pods
	trafficTraefikRouteIDs := make([]string, 0)
	trafficTraefikRouteIDMap := make(map[string]string)   // prefix:ns/name → ID
	trafficTraefikServiceIDMap := make(map[string]string) // ns/name → ID
	trafficMiddlewareIDMap := make(map[string]string)     // prefix:ns/name → ID
	trafficTraefikEdgeSeen := make(map[string]bool)

	// TraefikService nodes — Phase 1: create all nodes and populate ID map
	for _, ts := range trafficTraefikServices {
		ns := ts.GetNamespace()
		if !opts.MatchesNamespaceFilter(ns) {
			continue
		}
		name := ts.GetName()
		tsID := fmt.Sprintf("traefikservice/%s/%s", ns, name)
		trafficTraefikServiceIDMap[ns+"/"+name] = tsID

		spec, _, _ := unstructured.NestedMap(ts.Object, "spec")
		tsType := "unknown"
		if spec != nil {
			if _, ok := spec["weighted"]; ok {
				tsType = "weighted"
			} else if _, ok := spec["mirroring"]; ok {
				tsType = "mirroring"
			} else if _, ok := spec["highestRandomWeight"]; ok {
				tsType = "highestRandomWeight"
			}
		}

		nodes = append(nodes, Node{
			ID:     tsID,
			Kind:   KindTraefikService,
			Name:   name,
			Status: StatusHealthy,
			Data: map[string]any{
				"namespace":      ns,
				"traefikSvcType": tsType,
				"labels":         ts.GetLabels(),
				"apiVersion":     ts.GetAPIVersion(),
			},
		})
	}

	// TraefikService edges — Phase 2: all IDs populated, safe to resolve forward references
	for _, ts := range trafficTraefikServices {
		ns := ts.GetNamespace()
		if !opts.MatchesNamespaceFilter(ns) {
			continue
		}
		name := ts.GetName()
		tsID := trafficTraefikServiceIDMap[ns+"/"+name]
		if tsID == "" {
			continue
		}

		spec, _, _ := unstructured.NestedMap(ts.Object, "spec")
		if spec != nil {
			// Collect service refs from weighted and highestRandomWeight
			for _, svcListKey := range []string{"weighted", "highestRandomWeight"} {
				svcList, _, _ := unstructured.NestedSlice(spec, svcListKey, "services")
				for _, svc := range svcList {
					svcMap, ok := svc.(map[string]any)
					if !ok {
						continue
					}
					svcName, _ := svcMap["name"].(string)
					svcKind, _ := svcMap["kind"].(string)
					if svcName == "" {
						continue
					}
					svcNs, _ := svcMap["namespace"].(string)
					if svcNs == "" {
						svcNs = ns
					}
					if svcKind == "TraefikService" {
						targetID := trafficTraefikServiceIDMap[svcNs+"/"+svcName]
						if targetID != "" && targetID != tsID {
							dedupeKey := tsID + "|" + targetID
							if !trafficTraefikEdgeSeen[dedupeKey] {
								trafficTraefikEdgeSeen[dedupeKey] = true
								edges = append(edges, Edge{
									ID:     fmt.Sprintf("%s-to-%s", tsID, targetID),
									Source: tsID,
									Target: targetID,
									Type:   EdgeExposes,
								})
							}
						}
					} else if svcKind == "" || svcKind == "Service" {
						svcKey := svcNs + "/" + svcName
						if _, ok := servicesToInclude[svcKey]; ok {
							svcID := fmt.Sprintf("service/%s/%s", svcNs, svcName)
							serviceIDs[svcKey] = svcID
							dedupeKey := tsID + "|" + svcID
							if !trafficTraefikEdgeSeen[dedupeKey] {
								trafficTraefikEdgeSeen[dedupeKey] = true
								edges = append(edges, Edge{
									ID:     fmt.Sprintf("%s-to-%s", tsID, svcID),
									Source: tsID,
									Target: svcID,
									Type:   EdgeExposes,
								})
							}
						}
					}
				}
			}
			// mirroring: primary service + mirror targets
			mirrorBody, _, _ := unstructured.NestedMap(spec, "mirroring")
			if mirrorBody != nil {
				// Collect all mirroring refs (primary + mirrors) into one slice
				type mirrorRef struct {
					name, kind, namespace string
				}
				var mirrorRefs []mirrorRef
				bodyName, _ := mirrorBody["name"].(string)
				if bodyName != "" {
					bodyKind, _ := mirrorBody["kind"].(string)
					bodyNs, _ := mirrorBody["namespace"].(string)
					mirrorRefs = append(mirrorRefs, mirrorRef{bodyName, bodyKind, bodyNs})
				}
				mirrors, _, _ := unstructured.NestedSlice(mirrorBody, "mirrors")
				for _, m := range mirrors {
					if mm, ok := m.(map[string]any); ok {
						mName, _ := mm["name"].(string)
						if mName != "" {
							mKind, _ := mm["kind"].(string)
							mNs, _ := mm["namespace"].(string)
							mirrorRefs = append(mirrorRefs, mirrorRef{mName, mKind, mNs})
						}
					}
				}
				for _, ref := range mirrorRefs {
					refNs := ref.namespace
					if refNs == "" {
						refNs = ns
					}
					if ref.kind == "TraefikService" {
						targetID := trafficTraefikServiceIDMap[refNs+"/"+ref.name]
						if targetID != "" && targetID != tsID {
							dedupeKey := tsID + "|" + targetID
							if !trafficTraefikEdgeSeen[dedupeKey] {
								trafficTraefikEdgeSeen[dedupeKey] = true
								edges = append(edges, Edge{
									ID:     fmt.Sprintf("%s-to-%s", tsID, targetID),
									Source: tsID,
									Target: targetID,
									Type:   EdgeExposes,
								})
							}
						}
					} else if ref.kind == "" || ref.kind == "Service" {
						svcKey := refNs + "/" + ref.name
						if _, ok := servicesToInclude[svcKey]; ok {
							svcID := fmt.Sprintf("service/%s/%s", refNs, ref.name)
							serviceIDs[svcKey] = svcID
							dedupeKey := tsID + "|" + svcID
							if !trafficTraefikEdgeSeen[dedupeKey] {
								trafficTraefikEdgeSeen[dedupeKey] = true
								edges = append(edges, Edge{
									ID:     fmt.Sprintf("%s-to-%s", tsID, svcID),
									Source: tsID,
									Target: svcID,
									Type:   EdgeExposes,
								})
							}
						}
					}
				}
			}
		}
	}

	// Middleware nodes (traffic view - included for completeness)
	for _, mw := range trafficMiddlewares {
		ns := mw.GetNamespace()
		if !opts.MatchesNamespaceFilter(ns) {
			continue
		}
		name := mw.GetName()
		mwID := fmt.Sprintf("middleware/%s/%s", ns, name)
		trafficMiddlewareIDMap["middleware:"+ns+"/"+name] = mwID

		nodes = append(nodes, Node{
			ID:     mwID,
			Kind:   KindMiddleware,
			Name:   name,
			Status: extractGenericStatus(mw),
			Data: map[string]any{
				"namespace":  ns,
				"labels":     mw.GetLabels(),
				"apiVersion": mw.GetAPIVersion(),
			},
		})
	}
	for _, mw := range trafficMiddlewareTCPs {
		ns := mw.GetNamespace()
		if !opts.MatchesNamespaceFilter(ns) {
			continue
		}
		name := mw.GetName()
		mwID := fmt.Sprintf("middlewaretcp/%s/%s", ns, name)
		trafficMiddlewareIDMap["middlewaretcp:"+ns+"/"+name] = mwID

		nodes = append(nodes, Node{
			ID:     mwID,
			Kind:   KindMiddlewareTCP,
			Name:   name,
			Status: extractGenericStatus(mw),
			Data: map[string]any{
				"namespace":  ns,
				"labels":     mw.GetLabels(),
				"apiVersion": mw.GetAPIVersion(),
			},
		})
	}

	// IngressRoute nodes and edges (after TraefikService/Middleware so maps are populated)
	for i, rt := range trafficTraefikRoutes {
		ns := rt.GetNamespace()
		if !opts.MatchesNamespaceFilter(ns) {
			continue
		}
		name := rt.GetName()
		routeKind := trafficTraefikRouteKinds[i]
		kindLower := strings.ToLower(routeKind)
		routeID := fmt.Sprintf("%s/%s/%s", kindLower, ns, name)
		trafficTraefikRouteIDs = append(trafficTraefikRouteIDs, routeID)
		trafficTraefikRouteIDMap[kindLower+":"+ns+"/"+name] = routeID

		routes, _, _ := unstructured.NestedSlice(rt.Object, "spec", "routes")
		entryPoints, _, _ := unstructured.NestedStringSlice(rt.Object, "spec", "entryPoints")
		totalSvcCount := 0
		for _, route := range routes {
			routeMap, ok := route.(map[string]any)
			if !ok {
				continue
			}
			svcs, _, _ := unstructured.NestedSlice(routeMap, "services")
			totalSvcCount += len(svcs)
		}

		nodeStatus := StatusHealthy
		if len(routes) == 0 || totalSvcCount == 0 {
			nodeStatus = StatusUnhealthy
		}

		nodes = append(nodes, Node{
			ID:     routeID,
			Kind:   NodeKind(routeKind),
			Name:   name,
			Status: nodeStatus,
			Data: map[string]any{
				"namespace":    ns,
				"routeCount":   len(routes),
				"serviceCount": totalSvcCount,
				"entryPoints":  entryPoints,
				"labels":       rt.GetLabels(),
				"apiVersion":   rt.GetAPIVersion(),
			},
		})

		// IngressRoute → Service and IngressRoute → TraefikService edges
		for _, route := range routes {
			routeMap, ok := route.(map[string]any)
			if !ok {
				continue
			}
			svcs, _, _ := unstructured.NestedSlice(routeMap, "services")
			for _, svc := range svcs {
				svcMap, ok := svc.(map[string]any)
				if !ok {
					continue
				}
				svcName, _ := svcMap["name"].(string)
				if svcName == "" {
					continue
				}
				svcKind, _ := svcMap["kind"].(string)
				svcNs, _ := svcMap["namespace"].(string)
				if svcNs == "" {
					svcNs = ns
				}

				if svcKind == "TraefikService" {
					targetID := trafficTraefikServiceIDMap[svcNs+"/"+svcName]
					if targetID != "" {
						dedupeKey := routeID + "|" + targetID
						if !trafficTraefikEdgeSeen[dedupeKey] {
							trafficTraefikEdgeSeen[dedupeKey] = true
							edges = append(edges, Edge{
								ID:     fmt.Sprintf("%s-to-%s", routeID, targetID),
								Source: routeID,
								Target: targetID,
								Type:   EdgeExposes,
							})
						}
					}
				} else if svcKind == "" || svcKind == "Service" {
					svcKey := svcNs + "/" + svcName
					if _, ok := servicesToInclude[svcKey]; ok {
						svcID := fmt.Sprintf("service/%s/%s", svcNs, svcName)
						serviceIDs[svcKey] = svcID
						dedupeKey := routeID + "|" + svcID
						if !trafficTraefikEdgeSeen[dedupeKey] {
							trafficTraefikEdgeSeen[dedupeKey] = true
							edges = append(edges, Edge{
								ID:     fmt.Sprintf("%s-to-%s", routeID, svcID),
								Source: routeID,
								Target: svcID,
								Type:   EdgeExposes,
							})
						}
					}
				}
			}

			// Route → Middleware edges
			middlewares, _, _ := unstructured.NestedSlice(routeMap, "middlewares")
			for _, mw := range middlewares {
				mwMap, ok := mw.(map[string]any)
				if !ok {
					continue
				}
				mwName, _ := mwMap["name"].(string)
				if mwName == "" {
					continue
				}
				mwNs, _ := mwMap["namespace"].(string)
				if mwNs == "" {
					mwNs = ns
				}
				mwPrefix := "middleware"
				if routeKind == "IngressRouteTCP" {
					mwPrefix = "middlewaretcp"
				}
				mwID := trafficMiddlewareIDMap[mwPrefix+":"+mwNs+"/"+mwName]
				if mwID != "" {
					dedupeKey := routeID + "|" + mwID
					if !trafficTraefikEdgeSeen[dedupeKey] {
						trafficTraefikEdgeSeen[dedupeKey] = true
						edges = append(edges, Edge{
							ID:     fmt.Sprintf("%s-to-%s", routeID, mwID),
							Source: routeID,
							Target: mwID,
							Type:   EdgeConfigures,
						})
					}
				}
			}
		}
	}

	// Step 3f: Build Contour HTTPProxy nodes/edges for traffic view
	// Traffic flow: Internet → HTTPProxy (root) → HTTPProxy (child) → Service → Pods
	trafficHTTPProxyIDs := make([]string, 0)
	trafficHTTPProxyIDMap := make(map[string]string) // ns/name → ID
	trafficContourEdgeSeen := make(map[string]bool)

	// Phase 1: Create all HTTPProxy nodes and populate ID map
	for _, hp := range trafficHTTPProxies {
		ns := hp.GetNamespace()
		if !opts.MatchesNamespaceFilter(ns) {
			continue
		}
		name := hp.GetName()
		hpID := fmt.Sprintf("httpproxy/%s/%s", ns, name)
		trafficHTTPProxyIDMap[ns+"/"+name] = hpID

		fqdn, _, _ := unstructured.NestedString(hp.Object, "spec", "virtualhost", "fqdn")
		routes, _, _ := unstructured.NestedSlice(hp.Object, "spec", "routes")
		includes, _, _ := unstructured.NestedSlice(hp.Object, "spec", "includes")
		_, hasTLS, _ := unstructured.NestedMap(hp.Object, "spec", "virtualhost", "tls")

		nodeStatus := extractGenericStatus(hp)
		currentStatus, _, _ := unstructured.NestedString(hp.Object, "status", "currentStatus")
		switch strings.ToLower(currentStatus) {
		case "valid":
			nodeStatus = StatusHealthy
		case "invalid":
			nodeStatus = StatusUnhealthy
		case "orphaned":
			nodeStatus = StatusDegraded
		}

		// Root proxies have spec.virtualhost set
		isRoot := fqdn != ""
		if isRoot {
			trafficHTTPProxyIDs = append(trafficHTTPProxyIDs, hpID)
		}

		nodes = append(nodes, Node{
			ID:     hpID,
			Kind:   KindHTTPProxy,
			Name:   name,
			Status: nodeStatus,
			Data: map[string]any{
				"namespace":    ns,
				"fqdn":         fqdn,
				"routeCount":   len(routes),
				"includeCount": len(includes),
				"hasTLS":       hasTLS,
				"labels":       hp.GetLabels(),
				"apiVersion":   hp.GetAPIVersion(),
			},
		})
	}

	// Phase 2: Create edges (all IDs populated)
	for _, hp := range trafficHTTPProxies {
		ns := hp.GetNamespace()
		if !opts.MatchesNamespaceFilter(ns) {
			continue
		}
		name := hp.GetName()
		hpID := trafficHTTPProxyIDMap[ns+"/"+name]
		if hpID == "" {
			continue
		}

		// HTTPProxy → Service edges (via spec.routes[].services[])
		routes, _, _ := unstructured.NestedSlice(hp.Object, "spec", "routes")
		for _, route := range routes {
			routeMap, ok := route.(map[string]any)
			if !ok {
				continue
			}
			svcs, _, _ := unstructured.NestedSlice(routeMap, "services")
			for _, svc := range svcs {
				svcMap, ok := svc.(map[string]any)
				if !ok {
					continue
				}
				svcName, _ := svcMap["name"].(string)
				if svcName == "" {
					continue
				}
				svcNs := ns
				svcKey := svcNs + "/" + svcName
				if _, ok := servicesToInclude[svcKey]; ok {
					svcID := fmt.Sprintf("service/%s/%s", svcNs, svcName)
					serviceIDs[svcKey] = svcID
					dedupeKey := hpID + "|" + svcID
					if !trafficContourEdgeSeen[dedupeKey] {
						trafficContourEdgeSeen[dedupeKey] = true
						edges = append(edges, Edge{
							ID:     fmt.Sprintf("%s-to-%s", hpID, svcID),
							Source: hpID,
							Target: svcID,
							Type:   EdgeExposes,
						})
					}
				}
			}
		}

		// HTTPProxy → HTTPProxy edges (via spec.includes[])
		includes, _, _ := unstructured.NestedSlice(hp.Object, "spec", "includes")
		for _, incl := range includes {
			inclMap, ok := incl.(map[string]any)
			if !ok {
				continue
			}
			inclName, _ := inclMap["name"].(string)
			if inclName == "" {
				continue
			}
			inclNs, _ := inclMap["namespace"].(string)
			if inclNs == "" {
				inclNs = ns
			}
			targetID := trafficHTTPProxyIDMap[inclNs+"/"+inclName]
			if targetID == "" {
				continue
			}
			dedupeKey := hpID + "|" + targetID
			if !trafficContourEdgeSeen[dedupeKey] {
				trafficContourEdgeSeen[dedupeKey] = true
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", hpID, targetID),
					Source: hpID,
					Target: targetID,
					Type:   EdgeExposes,
				})
			}
		}

		// HTTPProxy → Service via tcpproxy (via spec.tcpproxy.services[])
		tcpSvcs, _, _ := unstructured.NestedSlice(hp.Object, "spec", "tcpproxy", "services")
		for _, svc := range tcpSvcs {
			svcMap, ok := svc.(map[string]any)
			if !ok {
				continue
			}
			svcName, _ := svcMap["name"].(string)
			if svcName == "" {
				continue
			}
			svcNs := ns
			svcKey := svcNs + "/" + svcName
			if _, ok := servicesToInclude[svcKey]; ok {
				svcID := fmt.Sprintf("service/%s/%s", svcNs, svcName)
				serviceIDs[svcKey] = svcID
				dedupeKey := hpID + "|" + svcID
				if !trafficContourEdgeSeen[dedupeKey] {
					trafficContourEdgeSeen[dedupeKey] = true
					edges = append(edges, Edge{
						ID:     fmt.Sprintf("%s-to-%s", hpID, svcID),
						Source: hpID,
						Target: svcID,
						Type:   EdgeExposes,
					})
				}
			}
		}
	}

	// Step 4: Add Internet node if we have ingresses, gateways, Istio gateways, or KNative services with URLs
	if len(ingressIDs) > 0 || len(trafficGatewayIDs) > 0 || len(trafficIstioGatewayIDs) > 0 || len(trafficKnativeServiceIDs) > 0 || len(trafficTraefikRouteIDs) > 0 || len(trafficHTTPProxyIDs) > 0 {
		nodes = append([]Node{{
			ID:     "internet",
			Kind:   KindInternet,
			Name:   "Internet",
			Status: StatusHealthy,
			Data:   map[string]any{},
		}}, nodes...)

		for _, ingID := range ingressIDs {
			edges = append(edges, Edge{
				ID:     fmt.Sprintf("internet-to-%s", ingID),
				Source: "internet",
				Target: ingID,
				Type:   EdgeRoutesTo,
			})
		}
		for _, gwID := range trafficGatewayIDs {
			edges = append(edges, Edge{
				ID:     fmt.Sprintf("internet-to-%s", gwID),
				Source: "internet",
				Target: gwID,
				Type:   EdgeRoutesTo,
			})
		}
		for _, igwID := range trafficIstioGatewayIDs {
			edges = append(edges, Edge{
				ID:     fmt.Sprintf("internet-to-%s", igwID),
				Source: "internet",
				Target: igwID,
				Type:   EdgeRoutesTo,
			})
		}
		for _, ksvcID := range trafficKnativeServiceIDs {
			edges = append(edges, Edge{
				ID:     fmt.Sprintf("internet-to-%s", ksvcID),
				Source: "internet",
				Target: ksvcID,
				Type:   EdgeRoutesTo,
			})
		}
		for _, trID := range trafficTraefikRouteIDs {
			edges = append(edges, Edge{
				ID:     fmt.Sprintf("internet-to-%s", trID),
				Source: "internet",
				Target: trID,
				Type:   EdgeRoutesTo,
			})
		}
		for _, hpID := range trafficHTTPProxyIDs {
			edges = append(edges, Edge{
				ID:     fmt.Sprintf("internet-to-%s", hpID),
				Source: "internet",
				Target: hpID,
				Type:   EdgeRoutesTo,
			})
		}
	}

	// Step 5: Add Service nodes (only included ones)
	for svcKey, svc := range servicesToInclude {
		svcID := fmt.Sprintf("service/%s/%s", svc.Namespace, svc.Name)
		serviceIDs[svcKey] = svcID

		var port int32
		if len(svc.Spec.Ports) > 0 {
			port = svc.Spec.Ports[0].Port
		}

		nodes = append(nodes, Node{
			ID:     svcID,
			Kind:   KindService,
			Name:   svc.Name,
			Status: StatusHealthy,
			Data: map[string]any{
				"namespace": svc.Namespace,
				"type":      string(svc.Spec.Type),
				"clusterIP": svc.Spec.ClusterIP,
				"port":      port,
				"labels":    svc.Labels,
			},
		})
	}

	// Step 6: Aggregate pods by owner and create PodGroup nodes
	// This prevents cluttering the graph with hundreds of individual pod nodes
	// Uses shared grouping logic with service matching for traffic view
	groupingResult := GroupPods(pods, PodGroupingOptions{
		Namespaces:      opts.Namespaces,
		ServiceMatching: true,
		ServicesByNS:    servicesByNS,
		ServiceIDs:      serviceIDs,
	})

	if opts.SummaryMode {
		// Summary mode: collapse the pod tier. In traffic view the routing
		// Service is the unit a user reasons about, so roll each group's pod
		// health onto every Service that routes to it. No Pod / PodGroup nodes
		// are emitted; the Service nodes (built above) carry the counts.
		podSummaries := make(map[string]*PodSummary)
		for _, group := range groupingResult.Groups {
			for svcID := range group.ServiceIDs {
				for _, pod := range group.Pods {
					addPodHealth(podSummaries, svcID, pod)
				}
			}
		}
		stampPodSummaries(nodes, podSummaries)
	} else {
		// Create nodes and edges for each group
		// Use MaxIndividualPods threshold to decide whether to show individual pods or group them
		maxIndividualPods := opts.MaxIndividualPods
		if maxIndividualPods <= 0 {
			maxIndividualPods = 5 // Default threshold
		}

		for _, group := range groupingResult.Groups {
			if len(group.Pods) <= maxIndividualPods {
				// Small group - show as individual nodes
				for _, pod := range group.Pods {
					podID := GetPodID(pod)
					nodes = append(nodes, CreatePodNode(pod, b.provider, false)) // includeNodeName=false for traffic view

					// Add edges from services to pod (traffic view specific)
					for svcID := range group.ServiceIDs {
						edges = append(edges, Edge{
							ID:     fmt.Sprintf("%s-to-%s", svcID, podID),
							Source: svcID,
							Target: podID,
							Type:   EdgeRoutesTo,
						})
					}
				}
			} else {
				// Large group - create PodGroup node
				podGroupID := GetPodGroupID(group)
				nodes = append(nodes, CreatePodGroupNode(group, b.provider))

				// Add edges from services to pod group (traffic view specific)
				for svcID := range group.ServiceIDs {
					edges = append(edges, Edge{
						ID:     fmt.Sprintf("%s-to-%s", svcID, podGroupID),
						Source: svcID,
						Target: podGroupID,
						Type:   EdgeRoutesTo,
					})
				}
			}
		}
	}

	topo := &Topology{Nodes: stampAuditKeys(nodes), Edges: edges, Warnings: warnings}

	// Add CRD discovery status
	if b.dynamic != nil {
		topo.CRDDiscoveryStatus = string(b.dynamic.GetDiscoveryStatus())
	}

	return topo, nil
}

// resolvePodWorkloadID returns the node ID of the top-level workload that owns
// the pod and that actually exists as a node in the current build — used by
// summary mode to attribute pod health counts. Returns "" when the pod has no
// resolvable workload node (standalone pods, bare ReplicaSets with no Deployment
// parent); those callers fall back to a collapsed PodGroup so the pods stay
// visible without flooding the graph.
//
// Mirrors the owner resolution in createPodOwnerEdges, but resolves through
// ReplicaSet → Deployment/Rollout (ReplicaSets are noisy intermediates hidden
// by default and not the unit a user reasons about at scale).
func (b *Builder) resolvePodWorkloadID(
	pod *corev1.Pod,
	existingNodeIDs map[string]bool,
	replicaSetToDeployment map[string]string,
	replicaSetToRollout map[string]string,
	jobIDs map[string]string,
	workflowIDs map[string]string,
) string {
	if workflowName := pod.Labels["workflows.argoproj.io/workflow"]; workflowName != "" {
		if workflowID, ok := workflowIDs[pod.Namespace+"/"+workflowName]; ok {
			return workflowID
		}
	}
	for _, ownerRef := range pod.OwnerReferences {
		if ownerRef.Controller == nil || !*ownerRef.Controller {
			continue
		}
		ownerKey := pod.Namespace + "/" + ownerRef.Name
		switch ownerRef.Kind {
		case "ReplicaSet":
			// These maps only hold IDs of nodes that were actually created.
			if deployID, ok := replicaSetToDeployment[ownerKey]; ok {
				return deployID
			}
			if rolloutID, ok := replicaSetToRollout[ownerKey]; ok {
				return rolloutID
			}
			return "" // bare ReplicaSet — no workload node to attribute to
		case "DaemonSet":
			// Unlike the map-backed cases, the DaemonSet/StatefulSet node ID is
			// synthesized from the owner ref — so it may name a node that was
			// never created (e.g. the controller list was denied by RBAC while
			// pods are listable). Gate on the real node set so those pods fall
			// through to the orphan PodGroup instead of vanishing.
			if id := fmt.Sprintf("daemonset/%s/%s", pod.Namespace, ownerRef.Name); existingNodeIDs[id] {
				return id
			}
			return ""
		case "StatefulSet":
			if id := fmt.Sprintf("statefulset/%s/%s", pod.Namespace, ownerRef.Name); existingNodeIDs[id] {
				return id
			}
			return ""
		case "Job":
			if jobID, ok := jobIDs[ownerKey]; ok {
				return jobID
			}
			return ""
		}
	}
	return ""
}

// addPodHealth accumulates a single pod's health into a PodSummary map keyed by
// node ID. Used by summary mode to roll pods up onto workloads/services.
func addPodHealth(summaries map[string]*PodSummary, nodeID string, pod *corev1.Pod) {
	s := summaries[nodeID]
	if s == nil {
		s = &PodSummary{}
		summaries[nodeID] = s
	}
	s.Total++
	switch podSummaryStatus(pod) {
	case StatusHealthy:
		s.Healthy++
	case StatusDegraded:
		s.Degraded++
	default:
		s.Unhealthy++
	}
}

// stampPodSummaries writes accumulated PodSummary counts onto the matching
// nodes' Data under "podSummary". Nodes is a value slice, so we mutate in place
// by index.
func stampPodSummaries(nodes []Node, summaries map[string]*PodSummary) {
	if len(summaries) == 0 {
		return
	}
	for i := range nodes {
		s, ok := summaries[nodes[i].ID]
		if !ok {
			continue
		}
		if nodes[i].Data == nil {
			nodes[i].Data = map[string]any{}
		}
		nodes[i].Data["podSummary"] = map[string]any{
			"total":     s.Total,
			"healthy":   s.Healthy,
			"degraded":  s.Degraded,
			"unhealthy": s.Unhealthy,
		}
	}
}

// Helper functions

// createPodOwnerEdges creates edges from a pod/podgroup to its owner(s)
// This is specific to the resources view which shows ownership hierarchy
func (b *Builder) createPodOwnerEdges(
	pod *corev1.Pod,
	targetID string, // podID or podGroupID
	opts BuildOptions,
	replicaSetIDs map[string]string,
	replicaSetToDeployment map[string]string,
	replicaSetToRollout map[string]string,
	jobIDs map[string]string,
	jobToCronJob map[string]string,
	jobToScaledJob map[string]string,
	workflowIDs map[string]string,
	workflowToCronWorkflow map[string]string,
) []Edge {
	var edges []Edge
	if workflowName := pod.Labels["workflows.argoproj.io/workflow"]; workflowName != "" {
		workflowKey := pod.Namespace + "/" + workflowName
		if ownerID, ok := workflowIDs[workflowKey]; ok {
			edges = append(edges, Edge{
				ID:     fmt.Sprintf("%s-to-%s", ownerID, targetID),
				Source: ownerID,
				Target: targetID,
				Type:   EdgeManages,
			})
			if cronWorkflowID, ok := workflowToCronWorkflow[workflowKey]; ok {
				edges = append(edges, Edge{
					ID:                fmt.Sprintf("%s-to-%s-shortcut", cronWorkflowID, targetID),
					Source:            cronWorkflowID,
					Target:            targetID,
					Type:              EdgeManages,
					SkipIfKindVisible: string(KindWorkflow),
				})
			}
			return edges
		}
	}

	for _, ownerRef := range pod.OwnerReferences {
		ownerKey := pod.Namespace + "/" + ownerRef.Name
		switch ownerRef.Kind {
		case "ReplicaSet":
			if opts.IncludeReplicaSets {
				// ReplicaSets visible: connect to ReplicaSet
				if ownerID, ok := replicaSetIDs[ownerKey]; ok {
					edges = append(edges, Edge{
						ID:     fmt.Sprintf("%s-to-%s", ownerID, targetID),
						Source: ownerID,
						Target: targetID,
						Type:   EdgeManages,
					})
				}
			} else {
				// ReplicaSets hidden: use shortcut edge directly to Deployment or Rollout
				if deployID, ok := replicaSetToDeployment[ownerKey]; ok {
					edges = append(edges, Edge{
						ID:     fmt.Sprintf("%s-to-%s", deployID, targetID),
						Source: deployID,
						Target: targetID,
						Type:   EdgeManages,
					})
				} else if rolloutID, ok := replicaSetToRollout[ownerKey]; ok {
					edges = append(edges, Edge{
						ID:     fmt.Sprintf("%s-to-%s", rolloutID, targetID),
						Source: rolloutID,
						Target: targetID,
						Type:   EdgeManages,
					})
				}
			}
		case "DaemonSet":
			ownerID := fmt.Sprintf("daemonset/%s/%s", pod.Namespace, ownerRef.Name)
			edges = append(edges, Edge{
				ID:     fmt.Sprintf("%s-to-%s", ownerID, targetID),
				Source: ownerID,
				Target: targetID,
				Type:   EdgeManages,
			})
		case "StatefulSet":
			ownerID := fmt.Sprintf("statefulset/%s/%s", pod.Namespace, ownerRef.Name)
			edges = append(edges, Edge{
				ID:     fmt.Sprintf("%s-to-%s", ownerID, targetID),
				Source: ownerID,
				Target: targetID,
				Type:   EdgeManages,
			})
		case "Job":
			if ownerID, ok := jobIDs[ownerKey]; ok {
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", ownerID, targetID),
					Source: ownerID,
					Target: targetID,
					Type:   EdgeManages,
				})
				// Add shortcut edge: CronJob -> Pod/PodGroup (for when Job is filtered out)
				if cronJobID, ok := jobToCronJob[ownerKey]; ok {
					edges = append(edges, Edge{
						ID:                fmt.Sprintf("%s-to-%s-shortcut", cronJobID, targetID),
						Source:            cronJobID,
						Target:            targetID,
						Type:              EdgeManages,
						SkipIfKindVisible: string(KindJob),
					})
				}
				if scaledJobID, ok := jobToScaledJob[ownerKey]; ok {
					edges = append(edges, Edge{
						ID:                fmt.Sprintf("%s-to-%s-shortcut", scaledJobID, targetID),
						Source:            scaledJobID,
						Target:            targetID,
						Type:              EdgeManages,
						SkipIfKindVisible: string(KindJob),
					})
				}
			}
		}
	}

	return edges
}

// getPodStatus returns the node-coloring health of a pod via the canonical
// classifier. It inspects container state (crashloop, OOM, fatal waiting), not
// just pod.Status.Phase — so a crashlooping pod (phase Running) now reads
// unhealthy here instead of healthy, matching the resource table.
func getPodStatus(pod *corev1.Pod) HealthStatus {
	return healthLevelToStatus(topoPodLevel(pod))
}

func getDeploymentStatus(ready, total int32) HealthStatus {
	if total == 0 {
		return StatusUnknown
	}
	if ready == total {
		return StatusHealthy
	}
	if ready > 0 {
		return StatusDegraded
	}
	return StatusUnhealthy
}

func getJobStatus(job *batchv1.Job) HealthStatus {
	return healthLevelToStatus(health.Workload(job, time.Now()).Level)
}

func workflowTopologyStatus(phase string) HealthStatus {
	switch phase {
	case "Succeeded":
		return StatusHealthy
	case "Running":
		return StatusNeutral
	case "Failed", "Error":
		return StatusUnhealthy
	case "Pending":
		return StatusDegraded
	default:
		return StatusUnknown
	}
}

func cronWorkflowTopologyStatus(cwf *unstructured.Unstructured) HealthStatus {
	suspended, _, _ := unstructured.NestedBool(cwf.Object, "spec", "suspend")
	if suspended {
		return StatusNeutral
	}
	active, found, _ := unstructured.NestedSlice(cwf.Object, "status", "active")
	if found && len(active) > 0 {
		return StatusNeutral
	}
	if lastScheduled, _, _ := unstructured.NestedString(cwf.Object, "status", "lastScheduledTime"); lastScheduled != "" {
		return StatusNeutral
	}
	return StatusUnknown
}

func cronWorkflowScheduleString(cwf *unstructured.Unstructured) string {
	schedules, found, _ := unstructured.NestedStringSlice(cwf.Object, "spec", "schedules")
	if found && len(schedules) > 0 {
		return strings.Join(schedules, ", ")
	}
	schedule, _, _ := unstructured.NestedString(cwf.Object, "spec", "schedule")
	return schedule
}

func argoWorkflowCronOwnerName(wf *unstructured.Unstructured) string {
	for _, ref := range wf.GetOwnerReferences() {
		if ref.Kind == "CronWorkflow" && ref.Name != "" && ref.Controller != nil && *ref.Controller {
			return ref.Name
		}
	}
	return wf.GetLabels()["workflows.argoproj.io/cron-workflow"]
}

type argoWorkflowTemplateRef struct {
	name         string
	clusterScope bool
}

func argoWorkflowTemplateRefsFromWorkflowSpec(obj map[string]any, fields ...string) []argoWorkflowTemplateRef {
	spec, found, _ := unstructured.NestedMap(obj, fields...)
	if !found {
		return nil
	}
	refs := make([]argoWorkflowTemplateRef, 0)
	seen := make(map[string]bool)
	addRef := func(raw map[string]any) {
		name, _, _ := unstructured.NestedString(raw, "name")
		if name == "" {
			return
		}
		clusterScope, _, _ := unstructured.NestedBool(raw, "clusterScope")
		key := fmt.Sprintf("%t/%s", clusterScope, name)
		if seen[key] {
			return
		}
		seen[key] = true
		refs = append(refs, argoWorkflowTemplateRef{name: name, clusterScope: clusterScope})
	}

	if workflowRef, found, _ := unstructured.NestedMap(spec, "workflowTemplateRef"); found {
		addRef(workflowRef)
	}
	templates, _, _ := unstructured.NestedSlice(spec, "templates")
	for _, rawTemplate := range templates {
		template, ok := rawTemplate.(map[string]any)
		if !ok {
			continue
		}
		for _, rawTask := range argoTemplateTasks(template) {
			task, ok := rawTask.(map[string]any)
			if !ok {
				continue
			}
			if templateRef, found, _ := unstructured.NestedMap(task, "templateRef"); found {
				addRef(templateRef)
			}
		}
	}
	return refs
}

func argoTemplateTasks(template map[string]any) []any {
	tasks := make([]any, 0)
	if dagTasks, found, _ := unstructured.NestedSlice(template, "dag", "tasks"); found {
		tasks = append(tasks, dagTasks...)
	}
	stepGroups, _, _ := unstructured.NestedSlice(template, "steps")
	for _, rawGroup := range stepGroups {
		if group, ok := rawGroup.([]any); ok {
			tasks = append(tasks, group...)
		}
	}
	return tasks
}

func addArgoWorkflowTemplateEdges(edges []Edge, sourceID, sourceNamespace string, refs []argoWorkflowTemplateRef, workflowTemplateIDs, clusterWorkflowTemplateIDs map[string]string) []Edge {
	for _, ref := range refs {
		targetID := ""
		if ref.clusterScope {
			targetID = clusterWorkflowTemplateIDs[ref.name]
		} else {
			targetID = workflowTemplateIDs[sourceNamespace+"/"+ref.name]
		}
		if targetID == "" {
			continue
		}
		edges = append(edges, Edge{
			ID:     fmt.Sprintf("%s-to-%s", targetID, sourceID),
			Source: targetID,
			Target: sourceID,
			Type:   EdgeConfigures,
		})
	}
	return edges
}

func getPVCStatus(pvc *corev1.PersistentVolumeClaim) HealthStatus {
	return healthLevelToStatus(health.Workload(pvc, time.Now()).Level)
}

// getFluxReadyStatus extracts the Ready condition status from a FluxCD resource's status map.
// Returns the ready status string ("True", "False", "Unknown") and the corresponding HealthStatus.
func getFluxReadyStatus(status map[string]any) (string, HealthStatus) {
	if status == nil {
		return "Unknown", StatusUnknown
	}
	conditions, ok, _ := unstructured.NestedSlice(status, "conditions")
	if !ok {
		return "Unknown", StatusUnknown
	}
	for _, c := range conditions {
		cond, ok := c.(map[string]any)
		if !ok || cond["type"] != "Ready" {
			continue
		}
		s, ok := cond["status"].(string)
		if !ok {
			return "Unknown", StatusUnknown
		}
		switch s {
		case "True":
			return s, StatusHealthy
		case "False":
			return s, StatusUnhealthy
		default:
			return s, StatusUnknown
		}
	}
	return "Unknown", StatusUnknown
}

func matchesSelector(labels, selector map[string]string) bool {
	if len(selector) == 0 {
		return false
	}
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func monitorLabelSelector(monitor *unstructured.Unstructured) (labels.Selector, bool, error) {
	raw, found, err := unstructured.NestedMap(monitor.Object, "spec", "selector")
	if err != nil || !found || len(raw) == 0 {
		return labels.Nothing(), true, err
	}
	var selector metav1.LabelSelector
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &selector); err != nil {
		return labels.Nothing(), false, err
	}
	parsed, err := metav1.LabelSelectorAsSelector(&selector)
	return parsed, len(selector.MatchLabels) == 0 && len(selector.MatchExpressions) == 0, err
}

func monitorSelectsNamespace(monitor *unstructured.Unstructured, namespace string) bool {
	anyNamespace, _, _ := unstructured.NestedBool(monitor.Object, "spec", "namespaceSelector", "any")
	if anyNamespace {
		return true
	}
	matchNames, _, _ := unstructured.NestedStringSlice(monitor.Object, "spec", "namespaceSelector", "matchNames")
	if len(matchNames) == 0 {
		return namespace == monitor.GetNamespace()
	}
	for _, selected := range matchNames {
		if selected == namespace {
			return true
		}
	}
	return false
}

func monitorNodeData(monitor *unstructured.Unstructured, endpointField string) map[string]any {
	endpoints, _, _ := unstructured.NestedSlice(monitor.Object, "spec", endpointField)
	return map[string]any{
		"namespace":     monitor.GetNamespace(),
		"labels":        monitor.GetLabels(),
		"apiVersion":    monitor.GetAPIVersion(),
		"endpointCount": len(endpoints),
	}
}

// matchesHelmRelease checks if a resource's labels indicate it's managed by a FluxCD HelmRelease
// Checks both FluxCD-specific labels and standard Helm labels
func matchesHelmRelease(labels map[string]string, hrName, hrNamespace string) bool {
	// FluxCD adds these labels to resources deployed by HelmRelease
	// helm.toolkit.fluxcd.io/name: <helmrelease-name>
	// helm.toolkit.fluxcd.io/namespace: <helmrelease-namespace>
	fluxName := labels["helm.toolkit.fluxcd.io/name"]
	fluxNS := labels["helm.toolkit.fluxcd.io/namespace"]
	if fluxName == hrName && (fluxNS == "" || fluxNS == hrNamespace) {
		return true
	}

	// Fallback to standard Helm label (app.kubernetes.io/instance)
	// This is set by charts that follow Helm best practices
	instanceLabel := labels["app.kubernetes.io/instance"]
	if instanceLabel == hrName {
		return true
	}

	return false
}

// parseIstioHost parses an Istio service host reference into service name and namespace.
// Istio hosts can be: "reviews", "reviews.default", "reviews.default.svc.cluster.local"
// Returns (serviceName, namespace). If no namespace is found, defaultNs is used.
func parseIstioHost(host, defaultNs string) (string, string) {
	parts := strings.Split(host, ".")
	if len(parts) == 1 {
		return parts[0], defaultNs
	}
	// "reviews.default" or "reviews.default.svc.cluster.local"
	return parts[0], parts[1]
}

// resolveKnativeRef resolves a KNative object reference (kind/ns/name) to a topology node ID.
// It checks K8s Services, KNative Services, Brokers, and Channels — the most common sink/subscriber targets.
func resolveKnativeRef(kind, ns, name string, serviceIDs, knativeServiceIDs, brokerIDs, channelIDs map[string]string) string {
	key := ns + "/" + name
	switch kind {
	case "Service":
		// Could be a K8s Service or a KNative Service — check KNative first (more specific)
		if id, ok := knativeServiceIDs[key]; ok {
			return id
		}
		if id, ok := serviceIDs[key]; ok {
			return id
		}
	case "Broker":
		if id, ok := brokerIDs[key]; ok {
			return id
		}
	case "Channel", "InMemoryChannel":
		if id, ok := channelIDs[key]; ok {
			return id
		}
	}
	return ""
}

type workloadRefs struct {
	configMaps     map[string]bool
	secrets        map[string]bool
	pvcs           map[string]bool
	serviceAccount string
}

func extractWorkloadReferences(spec corev1.PodSpec) workloadRefs {
	refs := workloadRefs{
		configMaps:     make(map[string]bool),
		secrets:        make(map[string]bool),
		pvcs:           make(map[string]bool),
		serviceAccount: spec.ServiceAccountName,
	}

	// From containers
	for _, container := range append(spec.Containers, spec.InitContainers...) {
		for _, env := range container.Env {
			if env.ValueFrom != nil {
				if env.ValueFrom.ConfigMapKeyRef != nil {
					refs.configMaps[env.ValueFrom.ConfigMapKeyRef.Name] = true
				}
				if env.ValueFrom.SecretKeyRef != nil {
					refs.secrets[env.ValueFrom.SecretKeyRef.Name] = true
				}
			}
		}
		for _, envFrom := range container.EnvFrom {
			if envFrom.ConfigMapRef != nil {
				refs.configMaps[envFrom.ConfigMapRef.Name] = true
			}
			if envFrom.SecretRef != nil {
				refs.secrets[envFrom.SecretRef.Name] = true
			}
		}
	}

	// From volumes
	for _, volume := range spec.Volumes {
		if volume.ConfigMap != nil {
			refs.configMaps[volume.ConfigMap.Name] = true
		}
		if volume.Secret != nil {
			refs.secrets[volume.Secret.SecretName] = true
		}
		if volume.PersistentVolumeClaim != nil {
			refs.pvcs[volume.PersistentVolumeClaim.ClaimName] = true
		}
	}

	return refs
}

// extractWorkloadReferencesFromMap extracts ConfigMap/Secret/PVC refs from unstructured pod spec
func extractWorkloadReferencesFromMap(spec map[string]any) workloadRefs {
	refs := workloadRefs{
		configMaps: make(map[string]bool),
		secrets:    make(map[string]bool),
		pvcs:       make(map[string]bool),
	}

	// Helper to get string from nested map
	getString := func(m map[string]any, key string) string {
		if v, ok := m[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		return ""
	}
	refs.serviceAccount = getString(spec, "serviceAccountName")

	// Process containers
	processContainers := func(containersField string) {
		containers, ok := spec[containersField].([]any)
		if !ok {
			return
		}
		for _, c := range containers {
			container, ok := c.(map[string]any)
			if !ok {
				continue
			}
			// Check env
			if env, ok := container["env"].([]any); ok {
				for _, e := range env {
					envVar, ok := e.(map[string]any)
					if !ok {
						continue
					}
					if valueFrom, ok := envVar["valueFrom"].(map[string]any); ok {
						if cmRef, ok := valueFrom["configMapKeyRef"].(map[string]any); ok {
							if name := getString(cmRef, "name"); name != "" {
								refs.configMaps[name] = true
							}
						}
						if secRef, ok := valueFrom["secretKeyRef"].(map[string]any); ok {
							if name := getString(secRef, "name"); name != "" {
								refs.secrets[name] = true
							}
						}
					}
				}
			}
			// Check envFrom
			if envFrom, ok := container["envFrom"].([]any); ok {
				for _, ef := range envFrom {
					envFromItem, ok := ef.(map[string]any)
					if !ok {
						continue
					}
					if cmRef, ok := envFromItem["configMapRef"].(map[string]any); ok {
						if name := getString(cmRef, "name"); name != "" {
							refs.configMaps[name] = true
						}
					}
					if secRef, ok := envFromItem["secretRef"].(map[string]any); ok {
						if name := getString(secRef, "name"); name != "" {
							refs.secrets[name] = true
						}
					}
				}
			}
		}
	}

	processContainers("containers")
	processContainers("initContainers")

	// Process volumes
	if volumes, ok := spec["volumes"].([]any); ok {
		for _, v := range volumes {
			volume, ok := v.(map[string]any)
			if !ok {
				continue
			}
			if cm, ok := volume["configMap"].(map[string]any); ok {
				if name := getString(cm, "name"); name != "" {
					refs.configMaps[name] = true
				}
			}
			if sec, ok := volume["secret"].(map[string]any); ok {
				if name := getString(sec, "secretName"); name != "" {
					refs.secrets[name] = true
				}
			}
			if pvc, ok := volume["persistentVolumeClaim"].(map[string]any); ok {
				if name := getString(pvc, "claimName"); name != "" {
					refs.pvcs[name] = true
				}
			}
		}
	}

	return refs
}

// getGatewayHealth derives Gateway health from status.conditions
// Programmed=True → healthy, Accepted=True (no Programmed) → degraded, conditions but neither → unhealthy, no conditions → unknown
func getGatewayHealth(gw *unstructured.Unstructured) HealthStatus {
	conditions, _, _ := unstructured.NestedSlice(gw.Object, "status", "conditions")
	hasProgrammed := false
	hasAccepted := false
	for _, c := range conditions {
		cMap, ok := c.(map[string]any)
		if !ok {
			continue
		}
		condType, _ := cMap["type"].(string)
		condStatus, _ := cMap["status"].(string)
		if condType == "Programmed" && condStatus == "True" {
			hasProgrammed = true
		}
		if condType == "Accepted" && condStatus == "True" {
			hasAccepted = true
		}
	}
	if hasProgrammed {
		return StatusHealthy
	}
	if hasAccepted {
		return StatusDegraded
	}
	if len(conditions) > 0 {
		return StatusUnhealthy
	}
	return StatusUnknown
}

// getRouteHealth derives route health from status.parents[].conditions
// All parents Accepted → healthy, some → degraded, none → unhealthy
func getRouteHealth(route *unstructured.Unstructured) HealthStatus {
	parents, _, _ := unstructured.NestedSlice(route.Object, "status", "parents")
	if len(parents) == 0 {
		return StatusUnknown
	}
	accepted := 0
	for _, p := range parents {
		pMap, ok := p.(map[string]any)
		if !ok {
			continue
		}
		conditions, _, _ := unstructured.NestedSlice(pMap, "conditions")
		for _, c := range conditions {
			cMap, ok := c.(map[string]any)
			if !ok {
				continue
			}
			if cMap["type"] == "Accepted" && cMap["status"] == "True" {
				accepted++
				break
			}
		}
	}
	if accepted == len(parents) {
		return StatusHealthy
	}
	if accepted > 0 {
		return StatusDegraded
	}
	return StatusUnhealthy
}

func sealedSecretHealth(resource *unstructured.Unstructured) HealthStatus {
	conditions, _, _ := unstructured.NestedSlice(resource.Object, "status", "conditions")
	if len(conditions) == 0 {
		return StatusUnknown
	}
	for _, condition := range conditions {
		conditionMap, ok := condition.(map[string]any)
		if !ok {
			continue
		}
		conditionType, _ := conditionMap["type"].(string)
		conditionStatus, _ := conditionMap["status"].(string)
		if conditionType == "Synced" && conditionStatus == "True" {
			return StatusHealthy
		}
		if conditionStatus == "False" {
			return StatusUnhealthy
		}
	}
	return StatusUnknown
}

// extractGenericStatus determines health from common CRD status patterns
func extractGenericStatus(resource *unstructured.Unstructured) HealthStatus {
	status, found, _ := unstructured.NestedMap(resource.Object, "status")
	if !found {
		return StatusUnknown
	}

	// Check conditions (most common pattern)
	if conditions, ok, _ := unstructured.NestedSlice(status, "conditions"); ok {
		for _, c := range conditions {
			if cond, ok := c.(map[string]any); ok {
				condType, _ := cond["type"].(string)
				if condType == "Ready" || condType == "Available" || condType == "Succeeded" {
					switch cond["status"] {
					case "True":
						return StatusHealthy
					case "False":
						return StatusUnhealthy
					case "Unknown":
						return StatusDegraded
					}
				}
			}
		}
	}

	// Check phase field
	if phase, ok, _ := unstructured.NestedString(status, "phase"); ok {
		switch strings.ToLower(phase) {
		case "running", "active", "ready", "succeeded", "bound":
			return StatusHealthy
		case "pending", "progressing":
			return StatusDegraded
		case "failed", "error":
			return StatusUnhealthy
		}
	}

	return StatusUnknown
}

// extractCertificateStatus reads the Ready condition from a cert-manager Certificate
func extractCertificateStatus(cert unstructured.Unstructured) HealthStatus {
	conditions, found, _ := unstructured.NestedSlice(cert.Object, "status", "conditions")
	if !found {
		return StatusUnknown
	}
	for _, c := range conditions {
		cond, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if cond["type"] == "Ready" {
			switch cond["status"] {
			case "True":
				return StatusHealthy
			case "False":
				return StatusUnhealthy
			}
			return StatusUnknown
		}
	}
	return StatusUnknown
}

// extractKarpenterNodePoolStatus reads the Ready condition from a Karpenter NodePool
func extractKarpenterNodePoolStatus(np unstructured.Unstructured) HealthStatus {
	conditions, found, _ := unstructured.NestedSlice(np.Object, "status", "conditions")
	if !found {
		return StatusUnknown
	}
	for _, c := range conditions {
		cond, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if cond["type"] == "Ready" {
			switch cond["status"] {
			case "True":
				return StatusHealthy
			case "False":
				return StatusUnhealthy
			}
			return StatusUnknown
		}
	}
	return StatusUnknown
}

// extractKarpenterNodeClaimStatus reads the Ready condition from a Karpenter NodeClaim
func extractKarpenterNodeClaimStatus(nc unstructured.Unstructured) HealthStatus {
	conditions, found, _ := unstructured.NestedSlice(nc.Object, "status", "conditions")
	if !found {
		return StatusUnknown
	}
	for _, c := range conditions {
		cond, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if cond["type"] == "Ready" {
			switch cond["status"] {
			case "True":
				return StatusHealthy
			case "False":
				return StatusUnhealthy
			}
			return StatusUnknown
		}
	}
	return StatusUnknown
}

// extractNodeStatus reads the Ready condition from a Kubernetes Node
func extractNodeStatus(node corev1.Node) HealthStatus {
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			if cond.Status != corev1.ConditionTrue {
				return StatusUnhealthy
			}
			// Ready but cordoned = lost scheduling capacity — degraded (amber),
			// matching the node table badge + drawer + Cordoned audit, so the same
			// node doesn't read green here while it's flagged elsewhere.
			if node.Spec.Unschedulable {
				return StatusDegraded
			}
			return StatusHealthy
		}
	}
	return StatusUnknown
}

// extractKedaScaledObjectStatus reads conditions and annotations from a KEDA ScaledObject
func extractKedaScaledObjectStatus(so unstructured.Unstructured) HealthStatus {
	// Check for Paused annotation (two variants). Paused = an operator
	// deliberately froze autoscaling — intentional, so neutral (sky), not amber.
	annotations := so.GetAnnotations()
	if annotations != nil {
		if paused, ok := annotations["autoscaling.keda.sh/paused"]; ok && paused == "true" {
			return StatusNeutral
		}
		if _, ok := annotations["autoscaling.keda.sh/paused-replicas"]; ok {
			return StatusNeutral
		}
	}

	conditions, found, _ := unstructured.NestedSlice(so.Object, "status", "conditions")
	if !found {
		return StatusUnknown
	}

	var activeCond, readyCond, fallbackCond map[string]any
	for _, c := range conditions {
		cond, ok := c.(map[string]any)
		if !ok {
			continue
		}
		switch cond["type"] {
		case "Fallback":
			fallbackCond = cond
		case "Ready":
			readyCond = cond
		case "Active":
			activeCond = cond
		}
	}

	// Fallback active means triggers are failing
	if fallbackCond != nil && fallbackCond["status"] == "True" {
		return StatusUnhealthy
	}

	// Ready=False means ScaledObject is not operational
	if readyCond != nil && readyCond["status"] == "False" {
		return StatusUnhealthy
	}

	if activeCond != nil {
		switch activeCond["status"] {
		case "True":
			return StatusHealthy
		case "False":
			// Idle (no triggers firing, scaled to zero) is the normal resting
			// state of a Ready scaler — intentional/off, so neutral (sky), not the
			// green of an actively-serving workload.
			return StatusNeutral
		}
	}

	if readyCond != nil && readyCond["status"] == "True" {
		return StatusHealthy
	}

	return StatusUnknown
}

// extractKedaScaledJobStatus reads conditions from a KEDA ScaledJob
func extractKedaScaledJobStatus(sj unstructured.Unstructured) HealthStatus {
	conditions, found, _ := unstructured.NestedSlice(sj.Object, "status", "conditions")
	if !found {
		return StatusUnknown
	}

	var activeCond, readyCond map[string]any
	for _, c := range conditions {
		cond, ok := c.(map[string]any)
		if !ok {
			continue
		}
		switch cond["type"] {
		case "Ready":
			readyCond = cond
		case "Active":
			activeCond = cond
		}
	}

	// Ready=False = not operational; surface it before the idle check.
	if readyCond != nil && readyCond["status"] == "False" {
		return StatusDegraded
	}

	// Check Active before treating Ready=True as healthy: an operational scaler
	// with no jobs running (Active=False) is intentionally idle → neutral (sky),
	// not the green of a busy one. (Ready=True first would make Idle unreachable.)
	if activeCond != nil {
		switch activeCond["status"] {
		case "True":
			return StatusHealthy
		case "False":
			return StatusNeutral
		}
	}

	if readyCond != nil && readyCond["status"] == "True" {
		return StatusHealthy
	}

	return StatusUnknown
}

// extractGatewayClassStatus reads the Accepted condition from a Gateway API GatewayClass
func extractGatewayClassStatus(gc unstructured.Unstructured) HealthStatus {
	conditions, found, _ := unstructured.NestedSlice(gc.Object, "status", "conditions")
	if !found {
		return StatusUnknown
	}
	for _, c := range conditions {
		cond, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if cond["type"] == "Accepted" {
			switch cond["status"] {
			case "True":
				return StatusHealthy
			case "False":
				return StatusUnhealthy
			}
			return StatusUnknown
		}
	}
	return StatusUnknown
}

// extractCAPIPhaseStatus reads status.phase from a CAPI resource and maps it to HealthStatus.
// Used for Cluster, Machine, MachineDeployment, MachineSet, MachinePool.
func extractCAPIPhaseStatus(obj unstructured.Unstructured) HealthStatus {
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	switch strings.ToLower(phase) {
	case "provisioned", "running", "ready", "scaled":
		return StatusHealthy
	case "provisioning", "pending", "scaling", "upgrading", "deleting":
		return StatusDegraded
	case "failed", "unknown":
		return StatusUnhealthy
	case "":
		// Fall back to Ready condition
		return extractCAPIReadyConditionStatus(obj)
	}
	return StatusUnknown
}

// extractCAPIReadyConditionStatus reads the Ready condition from a CAPI resource.
// Handles both v1beta1 (status.conditions) and v1beta2 (status.v1beta2.conditions) layouts.
func extractCAPIReadyConditionStatus(obj unstructured.Unstructured) HealthStatus {
	// Try v1beta2 conditions first
	conditions, found, _ := unstructured.NestedSlice(obj.Object, "status", "v1beta2", "conditions")
	if !found {
		conditions, found, _ = unstructured.NestedSlice(obj.Object, "status", "conditions")
	}
	if !found {
		return StatusUnknown
	}
	for _, c := range conditions {
		cond, ok := c.(map[string]any)
		if !ok {
			continue
		}
		condType, _ := cond["type"].(string)
		if condType == "Ready" || condType == "Available" {
			switch cond["status"] {
			case "True":
				return StatusHealthy
			case "False":
				return StatusUnhealthy
			}
			return StatusUnknown
		}
	}
	return StatusUnknown
}

// addGenericCRDNodes adds CRD nodes connected to the topology via owner references.
// It uses two-phase resolution: first collecting all candidate CRD resources, then
// iteratively adding nodes whose owners are already in the topology. This handles
// multi-level CRD chains (e.g., Certificate → CertificateRequest → Order) where
// intermediate nodes only become resolvable after their parents are added.
func (b *Builder) addGenericCRDNodes(nodes []Node, edges []Edge, opts BuildOptions) ([]Node, []Edge) {
	dynamicCache := b.dynamic
	resourceDiscovery := b.dynamic
	if dynamicCache == nil || resourceDiscovery == nil {
		return nodes, edges
	}

	// Build set of existing node IDs for fast lookup
	existingIDs := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		existingIDs[node.ID] = true
	}

	// Skip kinds handled explicitly by buildResourcesTopology or excluded from topology entirely
	processedKinds := map[string]bool{
		"rollout": true, "application": true, "kustomization": true,
		"helmrelease": true, "gitrepository": true, "certificate": true,
		"gateway": true, "httproute": true, "grpcroute": true, "tcproute": true, "tlsroute": true,
		"nodepool": true, "nodeclaim": true, // Karpenter
		"ec2nodeclass": true, "aksnodeclass": true, "gcenodeclass": true, // Karpenter NodeClass
		"scaledobject": true, "scaledjob": true, // KEDA
		"workflowtemplate": true, "clusterworkflowtemplate": true, // Argo Workflows
		"gatewayclass":   true,                                                // Gateway API
		"virtualservice": true, "destinationrule": true, "serviceentry": true, // Istio networking
		"peerauthentication": true, "authorizationpolicy": true, // Istio security
		"knativeservice": true, "configuration": true, "revision": true, "route": true, // KNative Serving
		"domainmapping": true, "serverlessservice": true, // KNative Serving (internal)
		"broker": true, "trigger": true, "eventtype": true, // KNative Eventing
		"channel": true, "inmemorychannel": true, "subscription": true, // KNative Messaging
		"apiserversource": true, "containersource": true, "pingsource": true, "sinkbinding": true, // KNative Sources
		"sequence": true, "parallel": true, // KNative Flows
		"ingressroute": true, "ingressroutetcp": true, "ingressrouteudp": true, // Traefik routing
		"middleware": true, "middlewaretcp": true, // Traefik middleware
		"traefikservice":   true,                              // Traefik service
		"serverstransport": true, "serverstransporttcp": true, // Traefik transport
		"tlsoption": true, "tlsstore": true, // Traefik TLS
		"httpproxy":    true,                                                // Contour
		"clusterclass": true,                                                // Cluster API
		"machine":      true, "machineset": true, "machinedeployment": true, // Cluster API
		"machinepool": true, "kubeadmcontrolplane": true, "machinehealthcheck": true, // Cluster API
		"machinedrainrule": true, // Cluster API
		// Trivy Operator reports - high cardinality, excluded from topology
		"vulnerabilityreport": true, "configauditreport": true,
		"exposedsecretreport": true, "sbomreport": true,
		"rbacassessmentreport": true, "clusterrbacassessmentreport": true,
		"clustercompliancereport": true, "clustersbomreport": true,
		"infraassessmentreport": true, "clusterinfraassessmentreport": true,
		// Core types handled by typed informers
		"deployment": true, "daemonset": true, "statefulset": true,
		"replicaset": true, "pod": true, "service": true, "ingress": true,
		"job": true, "cronjob": true, "configmap": true, "secret": true,
		"serviceaccount": true, "sealedsecret": true,
		"servicemonitor": true, "podmonitor": true,
		"persistentvolumeclaim": true, "horizontalpodautoscaler": true,
		// Argo Workflows handled explicitly above
		"workflow": true, "cronworkflow": true,
		// Also skip namespace (not typically owned)
		"namespace": true,
	}

	// Track per-kind counts to prevent any single CRD type from overwhelming the topology
	crdCounts := make(map[string]int)
	maxPerKind := 50

	// Phase 1: Collect all candidate CRD resources
	type candidate struct {
		nodeID    string
		node      Node
		ownerRefs []string // ownerKind/ns/name IDs
		ns        string
	}
	var candidates []candidate

	for _, gvr := range dynamicCache.GetWatchedResources() {
		kind := resourceDiscovery.GetKindForGVR(gvr)
		if kind == "" {
			continue
		}
		kindLower := strings.ToLower(kind)

		// Skip if already processed or not a CRD
		if processedKinds[kindLower] {
			continue
		}
		if !resourceDiscovery.IsCRD(kind) {
			continue
		}
		processedKinds[kindLower] = true

		resources, err := dynamicCache.ListNamespaces(gvr, opts.Namespaces)
		if err != nil {
			log.Printf("WARNING [topology] Failed to list %s resources for generic CRD support: %v", kind, err)
			continue
		}

		for _, resource := range resources {
			ns := resource.GetNamespace()
			if !opts.MatchesNamespaceFilter(ns) {
				continue
			}

			ownerRefs := resource.GetOwnerReferences()
			if len(ownerRefs) == 0 {
				continue
			}

			name := resource.GetName()
			nodeID := fmt.Sprintf("%s/%s/%s", kindLower, ns, name)

			// Skip if already in topology
			if existingIDs[nodeID] {
				continue
			}

			// Collect owner IDs
			var ownerNodeIDs []string
			for _, ref := range ownerRefs {
				ownerKindLower := strings.ToLower(ref.Kind)
				ownerNodeIDs = append(ownerNodeIDs, fmt.Sprintf("%s/%s/%s", ownerKindLower, ns, ref.Name))
			}

			candidates = append(candidates, candidate{
				nodeID: nodeID,
				node: Node{
					ID:     nodeID,
					Kind:   NodeKind(kind),
					Name:   name,
					Status: extractGenericStatus(resource),
					Data: map[string]any{
						"namespace":  ns,
						"labels":     resource.GetLabels(),
						"apiVersion": resource.GetAPIVersion(),
					},
				},
				ownerRefs: ownerNodeIDs,
				ns:        ns,
			})
		}
	}

	// Phase 2: Iterative resolution — keep adding nodes whose owners exist
	for {
		added := 0
		remaining := candidates[:0] // reuse slice
		for _, c := range candidates {
			kindLower := strings.ToLower(string(c.node.Kind))
			if crdCounts[kindLower] >= maxPerKind {
				continue // drop — kind at capacity
			}

			var ownerEdges []Edge
			for _, ownerID := range c.ownerRefs {
				if existingIDs[ownerID] {
					ownerEdges = append(ownerEdges, Edge{
						ID:     fmt.Sprintf("%s-to-%s", ownerID, c.nodeID),
						Source: ownerID,
						Target: c.nodeID,
						Type:   EdgeManages,
					})
				}
			}

			if len(ownerEdges) > 0 {
				nodes = append(nodes, c.node)
				edges = append(edges, ownerEdges...)
				existingIDs[c.nodeID] = true
				crdCounts[kindLower]++
				added++
			} else {
				remaining = append(remaining, c)
			}
		}
		candidates = remaining
		if added == 0 {
			break // No progress — stop
		}
	}

	return nodes, edges
}

// annotateNodePolicyCoverage adds "policyStatus" to workload node Data
// indicating whether the workload is selected by at least one network policy
// (standard NetworkPolicy, CiliumNetworkPolicy, or ClusterNetworkPolicy).
// Uses EdgeProtects edges — these are already computed for all policy types.
// Also checks standard NetworkPolicies with empty selectors (matchesAllPods)
// which don't create edges but still protect workloads.
func annotateNodePolicyCoverage(
	nodes []Node,
	edges []Edge,
	netpols []*networkingv1.NetworkPolicy,
	deployments []*appsv1.Deployment,
	statefulsets []*appsv1.StatefulSet,
	daemonsets []*appsv1.DaemonSet,
) {
	// Collect workloads covered by EdgeProtects edges (from any policy type)
	coveredWorkloads := make(map[string]bool)
	for _, e := range edges {
		if e.Type == EdgeProtects {
			coveredWorkloads[e.Target] = true
		}
	}

	// Also check standard NetworkPolicies with empty selectors (matchesAllPods).
	// These skip edge creation but still protect all workloads in their namespace.
	for _, np := range netpols {
		if np.Spec.PodSelector.Size() == 0 {
			for _, d := range deployments {
				if d.Namespace == np.Namespace {
					coveredWorkloads[fmt.Sprintf("deployment/%s/%s", d.Namespace, d.Name)] = true
				}
			}
			for _, s := range statefulsets {
				if s.Namespace == np.Namespace {
					coveredWorkloads[fmt.Sprintf("statefulset/%s/%s", s.Namespace, s.Name)] = true
				}
			}
			for _, d := range daemonsets {
				if d.Namespace == np.Namespace {
					coveredWorkloads[fmt.Sprintf("daemonset/%s/%s", d.Namespace, d.Name)] = true
				}
			}
		}
	}

	// Check CiliumNetworkPolicy/CiliumClusterwideNetworkPolicy nodes with matchesAllPods flag
	for _, n := range nodes {
		if (n.Kind == KindCiliumNetworkPolicy || n.Kind == KindCiliumClusterwideNetworkPolicy) && n.Data["matchesAllPods"] == true {
			ns, _ := n.Data["namespace"].(string)
			for _, d := range deployments {
				if ns == "" || d.Namespace == ns {
					coveredWorkloads[fmt.Sprintf("deployment/%s/%s", d.Namespace, d.Name)] = true
				}
			}
			for _, s := range statefulsets {
				if ns == "" || s.Namespace == ns {
					coveredWorkloads[fmt.Sprintf("statefulset/%s/%s", s.Namespace, s.Name)] = true
				}
			}
			for _, d := range daemonsets {
				if ns == "" || d.Namespace == ns {
					coveredWorkloads[fmt.Sprintf("daemonset/%s/%s", d.Namespace, d.Name)] = true
				}
			}
		}
	}

	// Annotate workload nodes
	for i := range nodes {
		n := &nodes[i]
		switch n.Kind {
		case KindDeployment, KindStatefulSet, KindDaemonSet:
			if coveredWorkloads[n.ID] {
				n.Data["policyStatus"] = "protected"
			} else {
				n.Data["policyStatus"] = "unprotected"
			}
		}
	}
}

// matchesStringMap checks if all key-value pairs in selector exist in labels.
// Used for CRD endpointSelector matching where the selector comes from unstructured data.
func matchesStringMap(labels map[string]string, selector map[string]any) bool {
	for k, v := range selector {
		sv, ok := v.(string)
		if !ok {
			return false // non-string selector value cannot match string labels
		}
		if labels[k] != sv {
			return false
		}
	}
	return true
}

// Unused but needed for imports
var _ = appsv1.Deployment{}
var _ = networkingv1.Ingress{}
var _ = strings.Contains
