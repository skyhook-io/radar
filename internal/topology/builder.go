package topology

import (
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/skyhook-explorer/internal/k8s"
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

	// Track IDs for linking
	deploymentIDs := make(map[string]string)
	statefulSetIDs := make(map[string]string)
	replicaSetIDs := make(map[string]string)
	replicaSetToDeployment := make(map[string]string) // rsKey -> deploymentID (for shortcut edges)
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
	if err == nil {
		for _, deploy := range deployments {
			if opts.Namespace != "" && deploy.Namespace != opts.Namespace {
				continue
			}

			deployID := fmt.Sprintf("deployment-%s-%s", deploy.Namespace, deploy.Name)
			deploymentIDs[deploy.Namespace+"/"+deploy.Name] = deployID

			ready := deploy.Status.ReadyReplicas
			total := *deploy.Spec.Replicas

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
	}

	// 2. Add DaemonSet nodes
	daemonsets, err := b.cache.DaemonSets().List(labels.Everything())
	if err == nil {
		for _, ds := range daemonsets {
			if opts.Namespace != "" && ds.Namespace != opts.Namespace {
				continue
			}

			dsID := fmt.Sprintf("daemonset-%s-%s", ds.Namespace, ds.Name)

			ready := ds.Status.NumberReady
			total := ds.Status.DesiredNumberScheduled

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
	}

	// 3. Add StatefulSet nodes
	statefulsets, err := b.cache.StatefulSets().List(labels.Everything())
	if err == nil {
		for _, sts := range statefulsets {
			if opts.Namespace != "" && sts.Namespace != opts.Namespace {
				continue
			}

			stsID := fmt.Sprintf("statefulset-%s-%s", sts.Namespace, sts.Name)
			statefulSetIDs[sts.Namespace+"/"+sts.Name] = stsID

			ready := sts.Status.ReadyReplicas
			total := *sts.Spec.Replicas

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
	}

	// 4. Add CronJob nodes
	cronjobs, err := b.cache.CronJobs().List(labels.Everything())
	if err == nil {
		for _, cj := range cronjobs {
			if opts.Namespace != "" && cj.Namespace != opts.Namespace {
				continue
			}

			cjID := fmt.Sprintf("cronjob-%s-%s", cj.Namespace, cj.Name)
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
	}

	// 5. Add Job nodes
	jobs, err := b.cache.Jobs().List(labels.Everything())
	if err == nil {
		for _, job := range jobs {
			if opts.Namespace != "" && job.Namespace != opts.Namespace {
				continue
			}

			jobID := fmt.Sprintf("job-%s-%s", job.Namespace, job.Name)
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
	}

	// 6. Add ReplicaSet nodes (active ones) - if enabled
	// Even if not shown, we still track them for shortcut edges
	replicasets, err := b.cache.ReplicaSets().List(labels.Everything())
	if err == nil {
		for _, rs := range replicasets {
			if opts.Namespace != "" && rs.Namespace != opts.Namespace {
				continue
			}

			// Skip inactive ReplicaSets (old rollouts)
			if rs.Spec.Replicas != nil && *rs.Spec.Replicas == 0 {
				continue
			}

			rsID := fmt.Sprintf("replicaset-%s-%s", rs.Namespace, rs.Name)
			replicaSetIDs[rs.Namespace+"/"+rs.Name] = rsID

			// Track owner for shortcut edges regardless of visibility
			for _, ownerRef := range rs.OwnerReferences {
				if ownerRef.Kind == "Deployment" {
					ownerKey := rs.Namespace + "/" + ownerRef.Name
					if ownerID, ok := deploymentIDs[ownerKey]; ok {
						rsKey := rs.Namespace + "/" + rs.Name
						replicaSetToDeployment[rsKey] = ownerID
					}
				}
			}

			// Only add node and edges if ReplicaSets are enabled
			if opts.IncludeReplicaSets {
				ready := rs.Status.ReadyReplicas
				total := *rs.Spec.Replicas

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

				// Connect to owner Deployment
				for _, ownerRef := range rs.OwnerReferences {
					if ownerRef.Kind == "Deployment" {
						ownerKey := rs.Namespace + "/" + ownerRef.Name
						if ownerID, ok := deploymentIDs[ownerKey]; ok {
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
		}
	}

	// 5. Add Pod nodes - grouped by app label when there are multiple pods
	pods, err := b.cache.Pods().List(labels.Everything())
	if err == nil {
		// Group pods by app label
		type resPodGroup struct {
			pods      []*corev1.Pod
			healthy   int
			degraded  int
			unhealthy int
		}
		podGroups := make(map[string]*resPodGroup) // key: namespace/app/appName

		for _, pod := range pods {
			if opts.Namespace != "" && pod.Namespace != opts.Namespace {
				continue
			}

			// Determine group key - prefer app label
			var groupKey string
			if appName := pod.Labels["app.kubernetes.io/name"]; appName != "" {
				groupKey = fmt.Sprintf("%s/app/%s", pod.Namespace, appName)
			} else if appName := pod.Labels["app"]; appName != "" {
				groupKey = fmt.Sprintf("%s/app/%s", pod.Namespace, appName)
			} else {
				// Fall back to owner reference
				ownerKind := "standalone"
				ownerName := pod.Name
				for _, ref := range pod.OwnerReferences {
					if ref.Controller != nil && *ref.Controller {
						ownerKind = ref.Kind
						ownerName = ref.Name
						break
					}
				}
				groupKey = fmt.Sprintf("%s/%s/%s", pod.Namespace, ownerKind, ownerName)
			}

			if _, exists := podGroups[groupKey]; !exists {
				podGroups[groupKey] = &resPodGroup{pods: make([]*corev1.Pod, 0)}
			}

			group := podGroups[groupKey]
			group.pods = append(group.pods, pod)

			status := getPodStatus(string(pod.Status.Phase))
			switch status {
			case StatusHealthy:
				group.healthy++
			case StatusDegraded:
				group.degraded++
			default:
				group.unhealthy++
			}
		}

		// Create nodes for each group
		for groupKey, group := range podGroups {
			if len(group.pods) == 1 {
				// Single pod - add as individual node
				pod := group.pods[0]
				podID := fmt.Sprintf("pod-%s-%s", pod.Namespace, pod.Name)

				restarts := int32(0)
				for _, cs := range pod.Status.ContainerStatuses {
					restarts += cs.RestartCount
				}

				nodes = append(nodes, Node{
					ID:     podID,
					Kind:   KindPod,
					Name:   pod.Name,
					Status: getPodStatus(string(pod.Status.Phase)),
					Data: map[string]any{
						"namespace":  pod.Namespace,
						"phase":      string(pod.Status.Phase),
						"restarts":   restarts,
						"containers": len(pod.Spec.Containers),
						"nodeName":   pod.Spec.NodeName,
						"labels":     pod.Labels,
					},
				})

				// Connect to owner
				for _, ownerRef := range pod.OwnerReferences {
					ownerKey := pod.Namespace + "/" + ownerRef.Name
					switch ownerRef.Kind {
					case "ReplicaSet":
						if opts.IncludeReplicaSets {
							// ReplicaSets visible: connect Pod to ReplicaSet
							if ownerID, ok := replicaSetIDs[ownerKey]; ok {
								edges = append(edges, Edge{
									ID:     fmt.Sprintf("%s-to-%s", ownerID, podID),
									Source: ownerID,
									Target: podID,
									Type:   EdgeManages,
								})
							}
						} else {
							// ReplicaSets hidden: use shortcut edge directly to Deployment
							if deployID, ok := replicaSetToDeployment[ownerKey]; ok {
								edges = append(edges, Edge{
									ID:     fmt.Sprintf("%s-to-%s", deployID, podID),
									Source: deployID,
									Target: podID,
									Type:   EdgeManages,
								})
							}
						}
					case "DaemonSet":
						ownerID := fmt.Sprintf("daemonset-%s-%s", pod.Namespace, ownerRef.Name)
						edges = append(edges, Edge{
							ID:     fmt.Sprintf("%s-to-%s", ownerID, podID),
							Source: ownerID,
							Target: podID,
							Type:   EdgeManages,
						})
					case "StatefulSet":
						ownerID := fmt.Sprintf("statefulset-%s-%s", pod.Namespace, ownerRef.Name)
						edges = append(edges, Edge{
							ID:     fmt.Sprintf("%s-to-%s", ownerID, podID),
							Source: ownerID,
							Target: podID,
							Type:   EdgeManages,
						})
					case "Job":
						if ownerID, ok := jobIDs[ownerKey]; ok {
							edges = append(edges, Edge{
								ID:     fmt.Sprintf("%s-to-%s", ownerID, podID),
								Source: ownerID,
								Target: podID,
								Type:   EdgeManages,
							})
							// Add shortcut edge: CronJob -> Pod (for when Job is filtered out)
							if cronJobID, ok := jobToCronJob[ownerKey]; ok {
								edges = append(edges, Edge{
									ID:                fmt.Sprintf("%s-to-%s-shortcut", cronJobID, podID),
									Source:            cronJobID,
									Target:            podID,
									Type:              EdgeManages,
									SkipIfKindVisible: string(KindJob),
								})
							}
						}
					}
				}
			} else {
				// Multiple pods - create PodGroup
				podGroupID := fmt.Sprintf("podgroup-%s", strings.ReplaceAll(groupKey, "/", "-"))

				// Get group name from first pod
				firstPod := group.pods[0]
				groupName := firstPod.Labels["app.kubernetes.io/name"]
				if groupName == "" {
					groupName = firstPod.Labels["app"]
				}
				if groupName == "" {
					// Use owner name
					for _, ref := range firstPod.OwnerReferences {
						if ref.Controller != nil && *ref.Controller {
							groupName = ref.Name
							break
						}
					}
				}
				if groupName == "" {
					groupName = "pods"
				}

				// Determine status
				var status HealthStatus
				if group.unhealthy > 0 {
					status = StatusUnhealthy
				} else if group.degraded > 0 {
					status = StatusDegraded
				} else {
					status = StatusHealthy
				}

				// Build pod details
				podDetails := make([]map[string]any, 0, len(group.pods))
				totalRestarts := int32(0)
				for _, pod := range group.pods {
					restarts := int32(0)
					for _, cs := range pod.Status.ContainerStatuses {
						restarts += cs.RestartCount
					}
					totalRestarts += restarts

					podDetails = append(podDetails, map[string]any{
						"name":       pod.Name,
						"namespace":  pod.Namespace,
						"phase":      string(pod.Status.Phase),
						"restarts":   restarts,
						"containers": len(pod.Spec.Containers),
					})
				}

				nodes = append(nodes, Node{
					ID:     podGroupID,
					Kind:   KindPodGroup,
					Name:   groupName,
					Status: status,
					Data: map[string]any{
						"namespace":     firstPod.Namespace,
						"podCount":      len(group.pods),
						"healthy":       group.healthy,
						"degraded":      group.degraded,
						"unhealthy":     group.unhealthy,
						"totalRestarts": totalRestarts,
						"pods":          podDetails,
					},
				})

				// Connect to owner (use first pod's owner)
				for _, ownerRef := range firstPod.OwnerReferences {
					ownerKey := firstPod.Namespace + "/" + ownerRef.Name
					switch ownerRef.Kind {
					case "ReplicaSet":
						if opts.IncludeReplicaSets {
							// ReplicaSets visible: connect PodGroup to ReplicaSet
							if ownerID, ok := replicaSetIDs[ownerKey]; ok {
								edges = append(edges, Edge{
									ID:     fmt.Sprintf("%s-to-%s", ownerID, podGroupID),
									Source: ownerID,
									Target: podGroupID,
									Type:   EdgeManages,
								})
							}
						} else {
							// ReplicaSets hidden: use shortcut edge directly to Deployment
							if deployID, ok := replicaSetToDeployment[ownerKey]; ok {
								edges = append(edges, Edge{
									ID:     fmt.Sprintf("%s-to-%s", deployID, podGroupID),
									Source: deployID,
									Target: podGroupID,
									Type:   EdgeManages,
								})
							}
						}
					case "DaemonSet":
						ownerID := fmt.Sprintf("daemonset-%s-%s", firstPod.Namespace, ownerRef.Name)
						edges = append(edges, Edge{
							ID:     fmt.Sprintf("%s-to-%s", ownerID, podGroupID),
							Source: ownerID,
							Target: podGroupID,
							Type:   EdgeManages,
						})
					case "StatefulSet":
						ownerID := fmt.Sprintf("statefulset-%s-%s", firstPod.Namespace, ownerRef.Name)
						edges = append(edges, Edge{
							ID:     fmt.Sprintf("%s-to-%s", ownerID, podGroupID),
							Source: ownerID,
							Target: podGroupID,
							Type:   EdgeManages,
						})
					case "Job":
						if ownerID, ok := jobIDs[ownerKey]; ok {
							edges = append(edges, Edge{
								ID:     fmt.Sprintf("%s-to-%s", ownerID, podGroupID),
								Source: ownerID,
								Target: podGroupID,
								Type:   EdgeManages,
							})
							// Add shortcut edge: CronJob -> PodGroup (for when Job is filtered out)
							if cronJobID, ok := jobToCronJob[ownerKey]; ok {
								edges = append(edges, Edge{
									ID:                fmt.Sprintf("%s-to-%s-shortcut", cronJobID, podGroupID),
									Source:            cronJobID,
									Target:            podGroupID,
									Type:              EdgeManages,
									SkipIfKindVisible: string(KindJob),
								})
							}
						}
					}
				}
			}
		}
	}

	// 8. Add Service nodes
	services, err := b.cache.Services().List(labels.Everything())
	if err == nil {
		for _, svc := range services {
			if opts.Namespace != "" && svc.Namespace != opts.Namespace {
				continue
			}

			svcID := fmt.Sprintf("service-%s-%s", svc.Namespace, svc.Name)
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

			// Connect Service to Deployments via selector
			if svc.Spec.Selector != nil {
				for _, deploy := range deployments {
					if deploy.Namespace != svc.Namespace {
						continue
					}
					if matchesSelector(deploy.Spec.Selector.MatchLabels, svc.Spec.Selector) {
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
		}
	}

	// 7. Add Ingress nodes
	ingresses, err := b.cache.Ingresses().List(labels.Everything())
	if err == nil {
		for _, ing := range ingresses {
			if opts.Namespace != "" && ing.Namespace != opts.Namespace {
				continue
			}

			ingID := fmt.Sprintf("ingress-%s-%s", ing.Namespace, ing.Name)

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
	}

	// 8. Add ConfigMap nodes (if enabled)
	if opts.IncludeConfigMaps {
		configmaps, err := b.cache.ConfigMaps().List(labels.Everything())
		if err == nil {
			for _, cm := range configmaps {
				if opts.Namespace != "" && cm.Namespace != opts.Namespace {
					continue
				}

				// Only include ConfigMaps that are referenced by workloads in the same namespace
				cmID := fmt.Sprintf("configmap-%s-%s", cm.Namespace, cm.Name)
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
	}

	// 9. Add Secret nodes (if enabled)
	if opts.IncludeSecrets {
		secrets, err := b.cache.Secrets().List(labels.Everything())
		if err == nil {
			for _, secret := range secrets {
				if opts.Namespace != "" && secret.Namespace != opts.Namespace {
					continue
				}

				// Only include Secrets that are referenced by workloads in the same namespace
				secretID := fmt.Sprintf("secret-%s-%s", secret.Namespace, secret.Name)
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
		if err == nil {
			for _, pvc := range pvcs {
				if opts.Namespace != "" && pvc.Namespace != opts.Namespace {
					continue
				}

				// Only include PVCs that are referenced by workloads in the same namespace
				pvcID := fmt.Sprintf("pvc-%s-%s", pvc.Namespace, pvc.Name)
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
	}

	// 11. Add HPA nodes
	hpas, err := b.cache.HorizontalPodAutoscalers().List(labels.Everything())
	if err == nil {
		for _, hpa := range hpas {
			if opts.Namespace != "" && hpa.Namespace != opts.Namespace {
				continue
			}

			hpaID := fmt.Sprintf("hpa-%s-%s", hpa.Namespace, hpa.Name)

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
	}

	return &Topology{Nodes: nodes, Edges: edges}, nil
}

// buildTrafficTopology creates a network-focused view
// Shows only nodes that are part of actual traffic paths: Internet -> Ingress -> Service -> Pod
func (b *Builder) buildTrafficTopology(opts BuildOptions) (*Topology, error) {
	nodes := make([]Node, 0)
	edges := make([]Edge, 0)

	// First, collect all raw data
	ingresses, _ := b.cache.Ingresses().List(labels.Everything())
	services, _ := b.cache.Services().List(labels.Everything())
	pods, _ := b.cache.Pods().List(labels.Everything())

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

		// Check if any pod matches this service's selector
		hasPods := false
		for _, pod := range pods {
			if pod.Namespace != svc.Namespace {
				continue
			}
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

	// Step 3: Build Ingress nodes and edges
	ingressIDs := make([]string, 0)
	for _, ing := range ingresses {
		if opts.Namespace != "" && ing.Namespace != opts.Namespace {
			continue
		}

		ingID := fmt.Sprintf("ingress-%s-%s", ing.Namespace, ing.Name)
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
						svcID := fmt.Sprintf("service-%s-%s", ing.Namespace, path.Backend.Service.Name)
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
		svcID := fmt.Sprintf("service-%s-%s", svc.Namespace, svc.Name)
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
	type podGroupInfo struct {
		ownerKind    string
		ownerName    string
		namespace    string
		pods         []*corev1.Pod
		serviceIDs   map[string]bool // services this group connects to
		healthy      int
		degraded     int
		unhealthy    int
	}

	podGroups := make(map[string]*podGroupInfo) // key: namespace/ownerKind/ownerName

	for _, pod := range pods {
		if opts.Namespace != "" && pod.Namespace != opts.Namespace {
			continue
		}

		// Find matching services that are included
		var matchingServiceIDs []string
		for svcKey, svc := range servicesToInclude {
			if svc.Namespace != pod.Namespace {
				continue
			}
			if matchesSelector(pod.Labels, svc.Spec.Selector) {
				matchingServiceIDs = append(matchingServiceIDs, serviceIDs[svcKey])
			}
		}

		// Skip pods with no service connections
		if len(matchingServiceIDs) == 0 {
			continue
		}

		// Determine pod's group key - prefer app label for broader grouping
		// This groups workflow pods, job pods, etc. by their logical app name
		var groupKey string
		var groupKind string
		var groupName string

		// First try app labels (groups all pods of the same app together)
		if appName := pod.Labels["app.kubernetes.io/name"]; appName != "" {
			groupKind = "app"
			groupName = appName
			groupKey = fmt.Sprintf("%s/app/%s", pod.Namespace, appName)
		} else if appName := pod.Labels["app"]; appName != "" {
			groupKind = "app"
			groupName = appName
			groupKey = fmt.Sprintf("%s/app/%s", pod.Namespace, appName)
		} else {
			// Fall back to owner reference (for pods without app labels)
			groupKind = "standalone"
			groupName = pod.Name
			for _, ref := range pod.OwnerReferences {
				if ref.Controller != nil && *ref.Controller {
					groupKind = ref.Kind
					groupName = ref.Name
					break
				}
			}
			groupKey = fmt.Sprintf("%s/%s/%s", pod.Namespace, groupKind, groupName)
		}

		if _, exists := podGroups[groupKey]; !exists {
			podGroups[groupKey] = &podGroupInfo{
				ownerKind:  groupKind,
				ownerName:  groupName,
				namespace:  pod.Namespace,
				pods:       make([]*corev1.Pod, 0),
				serviceIDs: make(map[string]bool),
			}
		}

		group := podGroups[groupKey]
		group.pods = append(group.pods, pod)

		// Track services
		for _, svcID := range matchingServiceIDs {
			group.serviceIDs[svcID] = true
		}

		// Track health
		status := getPodStatus(string(pod.Status.Phase))
		switch status {
		case StatusHealthy:
			group.healthy++
		case StatusDegraded:
			group.degraded++
		default:
			group.unhealthy++
		}
	}

	// Create PodGroup nodes for groups with multiple pods, individual nodes for singles
	for groupKey, group := range podGroups {
		if len(group.pods) == 1 {
			// Single pod - show as individual node
			pod := group.pods[0]
			podID := fmt.Sprintf("pod-%s-%s", pod.Namespace, pod.Name)

			restarts := int32(0)
			for _, cs := range pod.Status.ContainerStatuses {
				restarts += cs.RestartCount
			}

			nodes = append(nodes, Node{
				ID:     podID,
				Kind:   KindPod,
				Name:   pod.Name,
				Status: getPodStatus(string(pod.Status.Phase)),
				Data: map[string]any{
					"namespace":  pod.Namespace,
					"phase":      string(pod.Status.Phase),
					"restarts":   restarts,
					"containers": len(pod.Spec.Containers),
				},
			})

			// Add edges
			for svcID := range group.serviceIDs {
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", svcID, podID),
					Source: svcID,
					Target: podID,
					Type:   EdgeRoutesTo,
				})
			}
		} else {
			// Multiple pods - create PodGroup node
			podGroupID := fmt.Sprintf("podgroup-%s", strings.ReplaceAll(groupKey, "/", "-"))

			// Determine overall status
			var status HealthStatus
			if group.unhealthy > 0 {
				status = StatusUnhealthy
			} else if group.degraded > 0 {
				status = StatusDegraded
			} else {
				status = StatusHealthy
			}

			// Build pod details for frontend expansion
			podDetails := make([]map[string]any, 0, len(group.pods))
			totalRestarts := int32(0)
			for _, pod := range group.pods {
				restarts := int32(0)
				for _, cs := range pod.Status.ContainerStatuses {
					restarts += cs.RestartCount
				}
				totalRestarts += restarts

				podDetails = append(podDetails, map[string]any{
					"name":       pod.Name,
					"namespace":  pod.Namespace,
					"phase":      string(pod.Status.Phase),
					"restarts":   restarts,
					"containers": len(pod.Spec.Containers),
				})
			}

			nodes = append(nodes, Node{
				ID:     podGroupID,
				Kind:   KindPodGroup,
				Name:   group.ownerName,
				Status: status,
				Data: map[string]any{
					"namespace":     group.namespace,
					"ownerKind":     group.ownerKind,
					"podCount":      len(group.pods),
					"healthy":       group.healthy,
					"degraded":      group.degraded,
					"unhealthy":     group.unhealthy,
					"totalRestarts": totalRestarts,
					"pods":          podDetails, // For frontend expansion
				},
			})

			// Add edges from services to pod group
			for svcID := range group.serviceIDs {
				edges = append(edges, Edge{
					ID:     fmt.Sprintf("%s-to-%s", svcID, podGroupID),
					Source: svcID,
					Target: podGroupID,
					Type:   EdgeRoutesTo,
				})
			}
		}
	}

	return &Topology{Nodes: nodes, Edges: edges}, nil
}

// Helper functions

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

// Unused but needed for imports
var _ = appsv1.Deployment{}
var _ = networkingv1.Ingress{}
var _ = strings.Contains
