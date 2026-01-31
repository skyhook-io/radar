package topology

import (
	"fmt"
	"log"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/k8s"
)

// Builder constructs topology graphs from K8s resources
type Builder struct {
	cache *k8s.ResourceCache
}

// NewBuilder creates a new topology builder
func NewBuilder() *Builder {
	return &Builder{
		cache: k8s.GetResourceCache(),
	}
}

// Build constructs a topology based on the given options
func (b *Builder) Build(opts BuildOptions) (*Topology, error) {
	if b.cache == nil {
		return nil, fmt.Errorf("resource cache not initialized")
	}

	switch opts.ViewMode {
	case ViewModeTraffic:
		return b.buildTrafficTopology(opts)
	default:
		return b.buildResourcesTopology(opts)
	}
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

	// Track ConfigMap/Secret/PVC references from workloads
	// Maps workloadID -> set of resource names
	workloadConfigMapRefs := make(map[string]map[string]bool)
	workloadSecretRefs := make(map[string]map[string]bool)
	workloadPVCRefs := make(map[string]map[string]bool)
	// Track workload namespaces for cross-namespace validation
	workloadNamespaces := make(map[string]string) // workloadID -> namespace

	// 1. Add Deployment nodes
	deployments, err := b.cache.Deployments().List(labels.Everything())
	if err != nil {
		log.Printf("WARNING [topology] Failed to list Deployments: %v", err)
		warnings = append(warnings, fmt.Sprintf("Failed to list Deployments: %v", err))
	}
	for _, deploy := range deployments {
		if opts.Namespace != "" && deploy.Namespace != opts.Namespace {
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
		if resourceStatus := b.cache.GetResourceStatus("Deployment", deploy.Namespace, deploy.Name); resourceStatus != nil {
			statusSummary = resourceStatus.Summary
			statusIssue = resourceStatus.Issue
		}

		nodes = append(nodes, Node{
			ID:     deployID,
			Kind:   KindDeployment,
			Name:   deploy.Name,
			Status: getDeploymentStatus(ready, total),
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
		if len(refs.configMaps) > 0 || len(refs.secrets) > 0 || len(refs.pvcs) > 0 {
			workloadNamespaces[deployID] = deploy.Namespace
		}
		if len(refs.configMaps) > 0 {
			workloadConfigMapRefs[deployID] = refs.configMaps
		}
		if len(refs.secrets) > 0 {
			workloadSecretRefs[deployID] = refs.secrets
		}
		if len(refs.pvcs) > 0 {
			workloadPVCRefs[deployID] = refs.pvcs
		}
	}

	// 1b. Add Argo Rollout nodes (CRD - fetched via dynamic cache)
	dynamicCache := k8s.GetDynamicResourceCache()
	rolloutGVR, hasRollouts := k8s.GetResourceDiscovery().GetGVR("Rollout")
	if hasRollouts && dynamicCache != nil {
		rollouts, err := dynamicCache.List(rolloutGVR, opts.Namespace)
		if err != nil {
			log.Printf("WARNING [topology] Failed to list Rollouts: %v", err)
			warnings = append(warnings, fmt.Sprintf("Failed to list Rollouts: %v", err))
		}
		for _, rollout := range rollouts {
			ns := rollout.GetNamespace()
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
				},
			})

			// Extract pod template spec for config references
			template, _, _ := unstructured.NestedMap(spec, "template", "spec")
			if template != nil {
				refs := extractWorkloadReferencesFromMap(template)
				if len(refs.configMaps) > 0 || len(refs.secrets) > 0 || len(refs.pvcs) > 0 {
					workloadNamespaces[rolloutID] = ns
				}
				if len(refs.configMaps) > 0 {
					workloadConfigMapRefs[rolloutID] = refs.configMaps
				}
				if len(refs.secrets) > 0 {
					workloadSecretRefs[rolloutID] = refs.secrets
				}
				if len(refs.pvcs) > 0 {
					workloadPVCRefs[rolloutID] = refs.pvcs
				}
			}
		}
	}

	// 1c. Add ArgoCD Application nodes (CRD - fetched via dynamic cache)
	// Note: Application edges are created in a second pass after all resource IDs are populated
	applicationGVR, hasApplications := k8s.GetResourceDiscovery().GetGVR("Application")
	applicationIDs := make(map[string]string)                          // ns/name -> applicationID
	var applicationResources []*unstructured.Unstructured              // Store for second pass
	applicationDestNamespaces := make(map[string]string)               // appID -> destNamespace
	if hasApplications && dynamicCache != nil {
		applications, err := dynamicCache.List(applicationGVR, opts.Namespace)
		if err != nil {
			log.Printf("WARNING [topology] Failed to list ArgoCD Applications: %v", err)
			warnings = append(warnings, fmt.Sprintf("Failed to list ArgoCD Applications: %v", err))
		}
		for _, app := range applications {
			ns := app.GetNamespace()
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
					"namespace":         ns,
					"syncStatus":        syncStatus,
					"healthStatus":      healthStatus,
					"destination":       destination,
					"destNamespace":     destNamespace,
					"labels":            app.GetLabels(),
				},
			})

			// Store for second pass edge creation
			applicationResources = append(applicationResources, app)
			applicationDestNamespaces[appID] = destNamespace
		}
	}

	// 1d. Add FluxCD Kustomization nodes (CRD - fetched via dynamic cache)
	// Note: Kustomization edges are created in a second pass after all resource IDs are populated
	kustomizationGVR, hasKustomizations := k8s.GetResourceDiscovery().GetGVR("Kustomization")
	kustomizationIDs := make(map[string]string)               // ns/name -> kustomizationID
	var kustomizationResources []*unstructured.Unstructured   // Store for second pass
	if hasKustomizations && dynamicCache != nil {
		kustomizations, err := dynamicCache.List(kustomizationGVR, opts.Namespace)
		if err != nil {
			log.Printf("WARNING [topology] Failed to list FluxCD Kustomizations: %v", err)
			warnings = append(warnings, fmt.Sprintf("Failed to list FluxCD Kustomizations: %v", err))
		}
		for _, ks := range kustomizations {
			ns := ks.GetNamespace()
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
				},
			})

			// Store for second pass edge creation
			kustomizationResources = append(kustomizationResources, ks)
		}
	}

	// 1e. Add FluxCD GitRepository nodes (CRD - fetched via dynamic cache)
	gitRepoGVR, hasGitRepos := k8s.GetResourceDiscovery().GetGVR("GitRepository")
	gitRepoIDs := make(map[string]string) // ns/name -> gitRepoID
	if hasGitRepos && dynamicCache != nil {
		gitRepos, err := dynamicCache.List(gitRepoGVR, opts.Namespace)
		if err != nil {
			log.Printf("WARNING [topology] Failed to list FluxCD GitRepositories: %v", err)
			warnings = append(warnings, fmt.Sprintf("Failed to list FluxCD GitRepositories: %v", err))
		}
		for _, repo := range gitRepos {
			ns := repo.GetNamespace()
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
					"namespace": ns,
					"ready":     readyStatus,
					"branch":    branch,
					"url":       url,
					"labels":    repo.GetLabels(),
				},
			})
		}
	}

	// 1f. Add FluxCD HelmRelease nodes (CRD - fetched via dynamic cache)
	helmReleaseGVR, hasHelmReleases := k8s.GetResourceDiscovery().GetGVR("HelmRelease")
	helmReleaseIDs := make(map[string]string) // ns/name -> helmReleaseID
	if hasHelmReleases && dynamicCache != nil {
		helmReleases, err := dynamicCache.List(helmReleaseGVR, opts.Namespace)
		if err != nil {
			log.Printf("WARNING [topology] Failed to list FluxCD HelmReleases: %v", err)
			warnings = append(warnings, fmt.Sprintf("Failed to list FluxCD HelmReleases: %v", err))
		}
		for _, hr := range helmReleases {
			ns := hr.GetNamespace()
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
				},
			})
		}
	}

	// 2. Add DaemonSet nodes
	daemonsets, err := b.cache.DaemonSets().List(labels.Everything())
	if err != nil {
		log.Printf("WARNING [topology] Failed to list DaemonSets: %v", err)
		warnings = append(warnings, fmt.Sprintf("Failed to list DaemonSets: %v", err))
	}
	for _, ds := range daemonsets {
		if opts.Namespace != "" && ds.Namespace != opts.Namespace {
			continue
		}

		dsID := fmt.Sprintf("daemonset/%s/%s", ds.Namespace, ds.Name)

		ready := ds.Status.NumberReady
		total := ds.Status.DesiredNumberScheduled

		// Get status summary from cache for detailed issue reporting
		statusSummary := ""
		statusIssue := ""
		if resourceStatus := b.cache.GetResourceStatus("DaemonSet", ds.Namespace, ds.Name); resourceStatus != nil {
			statusSummary = resourceStatus.Summary
			statusIssue = resourceStatus.Issue
		}

		nodes = append(nodes, Node{
			ID:     dsID,
			Kind:   KindDaemonSet,
			Name:   ds.Name,
			Status: getDeploymentStatus(ready, total),
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
		if len(refs.configMaps) > 0 || len(refs.secrets) > 0 || len(refs.pvcs) > 0 {
			workloadNamespaces[dsID] = ds.Namespace
		}
		if len(refs.configMaps) > 0 {
			workloadConfigMapRefs[dsID] = refs.configMaps
		}
		if len(refs.secrets) > 0 {
			workloadSecretRefs[dsID] = refs.secrets
		}
		if len(refs.pvcs) > 0 {
			workloadPVCRefs[dsID] = refs.pvcs
		}
	}

	// 3. Add StatefulSet nodes
	statefulsets, err := b.cache.StatefulSets().List(labels.Everything())
	if err != nil {
		log.Printf("WARNING [topology] Failed to list StatefulSets: %v", err)
		warnings = append(warnings, fmt.Sprintf("Failed to list StatefulSets: %v", err))
	}
	for _, sts := range statefulsets {
		if opts.Namespace != "" && sts.Namespace != opts.Namespace {
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
		if resourceStatus := b.cache.GetResourceStatus("StatefulSet", sts.Namespace, sts.Name); resourceStatus != nil {
			statusSummary = resourceStatus.Summary
			statusIssue = resourceStatus.Issue
		}

		nodes = append(nodes, Node{
			ID:     stsID,
			Kind:   KindStatefulSet,
			Name:   sts.Name,
			Status: getDeploymentStatus(ready, total),
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
		if len(refs.configMaps) > 0 || len(refs.secrets) > 0 || len(refs.pvcs) > 0 {
			workloadNamespaces[stsID] = sts.Namespace
		}
		if len(refs.configMaps) > 0 {
			workloadConfigMapRefs[stsID] = refs.configMaps
		}
		if len(refs.secrets) > 0 {
			workloadSecretRefs[stsID] = refs.secrets
		}
		if len(refs.pvcs) > 0 {
			workloadPVCRefs[stsID] = refs.pvcs
		}
	}

	// 4. Add CronJob nodes
	cronjobs, err := b.cache.CronJobs().List(labels.Everything())
	if err != nil {
		log.Printf("WARNING [topology] Failed to list CronJobs: %v", err)
		warnings = append(warnings, fmt.Sprintf("Failed to list CronJobs: %v", err))
	}
	for _, cj := range cronjobs {
		if opts.Namespace != "" && cj.Namespace != opts.Namespace {
			continue
		}

		cjID := fmt.Sprintf("cronjob/%s/%s", cj.Namespace, cj.Name)
		cronJobIDs[cj.Namespace+"/"+cj.Name] = cjID

		// Determine status based on last schedule time and active jobs
		status := StatusHealthy
		if len(cj.Status.Active) > 0 {
			status = StatusDegraded // Running
		}

		nodes = append(nodes, Node{
			ID:     cjID,
			Kind:   KindCronJob,
			Name:   cj.Name,
			Status: status,
			Data: map[string]any{
				"namespace":        cj.Namespace,
				"schedule":         cj.Spec.Schedule,
				"suspend":          cj.Spec.Suspend != nil && *cj.Spec.Suspend,
				"activeJobs":       len(cj.Status.Active),
				"lastScheduleTime": cj.Status.LastScheduleTime,
				"labels":           cj.Labels,
			},
		})
	}

	// 5. Add Job nodes
	jobs, err := b.cache.Jobs().List(labels.Everything())
	if err != nil {
		log.Printf("WARNING [topology] Failed to list Jobs: %v", err)
		warnings = append(warnings, fmt.Sprintf("Failed to list Jobs: %v", err))
	}
	for _, job := range jobs {
		if opts.Namespace != "" && job.Namespace != opts.Namespace {
			continue
		}

		jobID := fmt.Sprintf("job/%s/%s", job.Namespace, job.Name)
		jobIDs[job.Namespace+"/"+job.Name] = jobID

		// Determine status
		status := getJobStatus(job)

		nodes = append(nodes, Node{
			ID:     jobID,
			Kind:   KindJob,
			Name:   job.Name,
			Status: status,
			Data: map[string]any{
				"namespace":   job.Namespace,
				"completions": job.Spec.Completions,
				"parallelism": job.Spec.Parallelism,
				"succeeded":   job.Status.Succeeded,
				"failed":      job.Status.Failed,
				"active":      job.Status.Active,
				"labels":      job.Labels,
			},
		})

		// Track ConfigMap/Secret/PVC references
		refs := extractWorkloadReferences(job.Spec.Template.Spec)
		if len(refs.configMaps) > 0 || len(refs.secrets) > 0 || len(refs.pvcs) > 0 {
			workloadNamespaces[jobID] = job.Namespace
		}
		if len(refs.configMaps) > 0 {
			workloadConfigMapRefs[jobID] = refs.configMaps
		}
		if len(refs.secrets) > 0 {
			workloadSecretRefs[jobID] = refs.secrets
		}
		if len(refs.pvcs) > 0 {
			workloadPVCRefs[jobID] = refs.pvcs
		}

		// Connect to owner CronJob
		for _, ownerRef := range job.OwnerReferences {
			if ownerRef.Kind == "CronJob" {
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
			}
		}
	}

	// 6. Add ReplicaSet nodes (active ones) - if enabled
	// Even if not shown, we still track them for shortcut edges
	replicasets, err := b.cache.ReplicaSets().List(labels.Everything())
	if err != nil {
		log.Printf("WARNING [topology] Failed to list ReplicaSets: %v", err)
		warnings = append(warnings, fmt.Sprintf("Failed to list ReplicaSets: %v", err))
	}
	for _, rs := range replicasets {
		if opts.Namespace != "" && rs.Namespace != opts.Namespace {
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
				Status: getDeploymentStatus(ready, total),
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
	pods, err := b.cache.Pods().List(labels.Everything())
	if err != nil {
		log.Printf("WARNING [topology] Failed to list Pods: %v", err)
		warnings = append(warnings, fmt.Sprintf("Failed to list Pods: %v", err))
	}
	if len(pods) > 0 {
		// Group pods using shared grouping logic
		groupingResult := GroupPods(pods, PodGroupingOptions{
			Namespace: opts.Namespace,
		})

		// Create nodes and edges for each group
		for _, group := range groupingResult.Groups {
			if len(group.Pods) == 1 {
				// Single pod - add as individual node
				pod := group.Pods[0]
				podID := GetPodID(pod)
				nodes = append(nodes, CreatePodNode(pod, b.cache, true)) // includeNodeName=true for resources view

				// Connect to owner (resources view specific)
				edges = append(edges, b.createPodOwnerEdges(pod, podID, opts, replicaSetIDs, replicaSetToDeployment, replicaSetToRollout, jobIDs, jobToCronJob)...)
			} else {
				// Multiple pods - create PodGroup
				podGroupID := GetPodGroupID(group)
				nodes = append(nodes, CreatePodGroupNode(group, b.cache))

				// Connect to owner using first pod's owner (resources view specific)
				firstPod := group.Pods[0]
				edges = append(edges, b.createPodOwnerEdges(firstPod, podGroupID, opts, replicaSetIDs, replicaSetToDeployment, replicaSetToRollout, jobIDs, jobToCronJob)...)
			}
		}
	}

	// 8. Add Service nodes
	services, err := b.cache.Services().List(labels.Everything())
	if err != nil {
		log.Printf("WARNING [topology] Failed to list Services: %v", err)
		warnings = append(warnings, fmt.Sprintf("Failed to list Services: %v", err))
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
		if opts.Namespace != "" && svc.Namespace != opts.Namespace {
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
		if hasRollouts && dynamicCache != nil {
			svcRollouts, rolloutErr := dynamicCache.List(rolloutGVR, svc.Namespace)
			if rolloutErr != nil {
				log.Printf("WARNING [topology] Failed to list Rollouts for service %s/%s: %v", svc.Namespace, svc.Name, rolloutErr)
				warnings = append(warnings, fmt.Sprintf("Failed to list Rollouts: %v", rolloutErr))
			}
			for _, rollout := range svcRollouts {
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
	}

	// 7. Add Ingress nodes
	ingresses, err := b.cache.Ingresses().List(labels.Everything())
	if err != nil {
		log.Printf("WARNING [topology] Failed to list Ingresses: %v", err)
		warnings = append(warnings, fmt.Sprintf("Failed to list Ingresses: %v", err))
	}
	for _, ing := range ingresses {
		if opts.Namespace != "" && ing.Namespace != opts.Namespace {
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

	// 8. Add ConfigMap nodes (if enabled)
	if opts.IncludeConfigMaps {
		configmaps, err := b.cache.ConfigMaps().List(labels.Everything())
		if err != nil {
			log.Printf("WARNING [topology] Failed to list ConfigMaps: %v", err)
			warnings = append(warnings, fmt.Sprintf("Failed to list ConfigMaps: %v", err))
		}
		for _, cm := range configmaps {
			if opts.Namespace != "" && cm.Namespace != opts.Namespace {
				continue
			}

			// Only include ConfigMaps that are referenced by workloads in the same namespace
			cmID := fmt.Sprintf("configmap/%s/%s", cm.Namespace, cm.Name)
			isReferenced := false

			for workloadID, refs := range workloadConfigMapRefs {
				// Only match if workload is in the same namespace as the ConfigMap
				if workloadNamespaces[workloadID] != cm.Namespace {
					continue
				}
				if refs[cm.Name] {
					isReferenced = true
					edges = append(edges, Edge{
						ID:     fmt.Sprintf("%s-to-%s", cmID, workloadID),
						Source: cmID,
						Target: workloadID,
						Type:   EdgeConfigures,
					})
				}
			}

			if isReferenced {
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
	if opts.IncludeSecrets {
		secretLister := b.cache.Secrets()
		if secretLister == nil {
			log.Printf("WARNING [topology] Secrets not available (RBAC not granted)")
			warnings = append(warnings, "Secrets not available (RBAC not granted)")
		} else {
			secrets, err := secretLister.List(labels.Everything())
			if err != nil {
				log.Printf("WARNING [topology] Failed to list Secrets: %v", err)
				warnings = append(warnings, fmt.Sprintf("Failed to list Secrets: %v", err))
			}
			for _, secret := range secrets {
				if opts.Namespace != "" && secret.Namespace != opts.Namespace {
					continue
				}

				// Only include Secrets that are referenced by workloads in the same namespace
				secretID := fmt.Sprintf("secret/%s/%s", secret.Namespace, secret.Name)
				isReferenced := false

				for workloadID, refs := range workloadSecretRefs {
					// Only match if workload is in the same namespace as the Secret
					if workloadNamespaces[workloadID] != secret.Namespace {
						continue
					}
					if refs[secret.Name] {
						isReferenced = true
						edges = append(edges, Edge{
							ID:     fmt.Sprintf("%s-to-%s", secretID, workloadID),
							Source: secretID,
							Target: workloadID,
							Type:   EdgeConfigures,
						})
					}
				}

				if isReferenced {
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
	}

	// 10. Add PVC nodes (if enabled)
	if opts.IncludePVCs {
		pvcs, err := b.cache.PersistentVolumeClaims().List(labels.Everything())
		if err != nil {
			log.Printf("WARNING [topology] Failed to list PersistentVolumeClaims: %v", err)
			warnings = append(warnings, fmt.Sprintf("Failed to list PersistentVolumeClaims: %v", err))
		}
		for _, pvc := range pvcs {
			if opts.Namespace != "" && pvc.Namespace != opts.Namespace {
				continue
			}

			// Only include PVCs that are referenced by workloads in the same namespace
			pvcID := fmt.Sprintf("pvc/%s/%s", pvc.Namespace, pvc.Name)
			isReferenced := false

			for workloadID, refs := range workloadPVCRefs {
				// Only match if workload is in the same namespace as the PVC
				if workloadNamespaces[workloadID] != pvc.Namespace {
					continue
				}
				if refs[pvc.Name] {
					isReferenced = true
					edges = append(edges, Edge{
						ID:     fmt.Sprintf("%s-to-%s", pvcID, workloadID),
						Source: pvcID,
						Target: workloadID,
						Type:   EdgeUses,
					})
				}
			}

			if isReferenced {
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
					Status: getPVCStatus(pvc.Status.Phase),
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
	hpas, err := b.cache.HorizontalPodAutoscalers().List(labels.Everything())
	if err != nil {
		log.Printf("WARNING [topology] Failed to list HorizontalPodAutoscalers: %v", err)
		warnings = append(warnings, fmt.Sprintf("Failed to list HorizontalPodAutoscalers: %v", err))
	}
	for _, hpa := range hpas {
		if opts.Namespace != "" && hpa.Namespace != opts.Namespace {
			continue
		}

		hpaID := fmt.Sprintf("hpa/%s/%s", hpa.Namespace, hpa.Name)

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
			dep, err := b.cache.Deployments().Deployments(depNS).Get(depName)
			if err != nil || dep == nil {
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
			svc, err := b.cache.Services().Services(svcNS).Get(svcName)
			if err != nil || svc == nil {
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
			sts, err := b.cache.StatefulSets().StatefulSets(stsNS).Get(stsName)
			if err != nil || sts == nil {
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
	}

	return &Topology{Nodes: nodes, Edges: edges, Warnings: warnings}, nil
}

// buildTrafficTopology creates a network-focused view
// Shows only nodes that are part of actual traffic paths: Internet -> Ingress -> Service -> Pod
func (b *Builder) buildTrafficTopology(opts BuildOptions) (*Topology, error) {
	nodes := make([]Node, 0)
	edges := make([]Edge, 0)
	warnings := make([]string, 0)

	// First, collect all raw data
	ingresses, err := b.cache.Ingresses().List(labels.Everything())
	if err != nil {
		log.Printf("WARNING [topology/traffic] Failed to list Ingresses: %v", err)
		warnings = append(warnings, fmt.Sprintf("Failed to list Ingresses: %v", err))
	}
	services, err := b.cache.Services().List(labels.Everything())
	if err != nil {
		log.Printf("WARNING [topology/traffic] Failed to list Services: %v", err)
		warnings = append(warnings, fmt.Sprintf("Failed to list Services: %v", err))
	}
	pods, err := b.cache.Pods().List(labels.Everything())
	if err != nil {
		log.Printf("WARNING [topology/traffic] Failed to list Pods: %v", err)
		warnings = append(warnings, fmt.Sprintf("Failed to list Pods: %v", err))
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

	// Step 1: Find services referenced by ingresses
	for _, ing := range ingresses {
		if opts.Namespace != "" && ing.Namespace != opts.Namespace {
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

	// Step 2: Find all services and check which have pods
	for _, svc := range services {
		if opts.Namespace != "" && svc.Namespace != opts.Namespace {
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

		// Include service if: referenced by ingress OR has matching pods
		if servicesFromIngress[svcKey] || hasPods {
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
		if opts.Namespace != "" && ing.Namespace != opts.Namespace {
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

	// Step 4: Add Internet node if we have ingresses
	if len(ingressIDs) > 0 {
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
		Namespace:       opts.Namespace,
		ServiceMatching: true,
		ServicesByNS:    servicesByNS,
		ServiceIDs:      serviceIDs,
	})

	// Create nodes and edges for each group
	for _, group := range groupingResult.Groups {
		if len(group.Pods) == 1 {
			// Single pod - show as individual node
			pod := group.Pods[0]
			podID := GetPodID(pod)
			nodes = append(nodes, CreatePodNode(pod, b.cache, false)) // includeNodeName=false for traffic view

			// Add edges from services to pod (traffic view specific)
			for svcID := range group.ServiceIDs {
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", svcID, podID),
					Source: svcID,
					Target: podID,
					Type:   EdgeRoutesTo,
				})
			}
		} else {
			// Multiple pods - create PodGroup node
			podGroupID := GetPodGroupID(group)
			nodes = append(nodes, CreatePodGroupNode(group, b.cache))

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

	return &Topology{Nodes: nodes, Edges: edges, Warnings: warnings}, nil
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
) []Edge {
	var edges []Edge

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
			}
		}
	}

	return edges
}

func getPodStatus(phase string) HealthStatus {
	switch phase {
	case "Running", "Succeeded":
		return StatusHealthy
	case "Pending":
		return StatusDegraded
	case "Failed", "CrashLoopBackOff":
		return StatusUnhealthy
	default:
		return StatusUnknown
	}
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
	// Check completion conditions
	for _, cond := range job.Status.Conditions {
		if cond.Type == batchv1.JobComplete && cond.Status == corev1.ConditionTrue {
			return StatusHealthy
		}
		if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue {
			return StatusUnhealthy
		}
	}
	// Still running
	if job.Status.Active > 0 {
		return StatusDegraded
	}
	return StatusUnknown
}

func getPVCStatus(phase corev1.PersistentVolumeClaimPhase) HealthStatus {
	switch phase {
	case corev1.ClaimBound:
		return StatusHealthy
	case corev1.ClaimPending:
		return StatusDegraded
	case corev1.ClaimLost:
		return StatusUnhealthy
	default:
		return StatusUnknown
	}
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

type workloadRefs struct {
	configMaps map[string]bool
	secrets    map[string]bool
	pvcs       map[string]bool
}

func extractWorkloadReferences(spec corev1.PodSpec) workloadRefs {
	refs := workloadRefs{
		configMaps: make(map[string]bool),
		secrets:    make(map[string]bool),
		pvcs:       make(map[string]bool),
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

// Unused but needed for imports
var _ = appsv1.Deployment{}
var _ = networkingv1.Ingress{}
var _ = strings.Contains
