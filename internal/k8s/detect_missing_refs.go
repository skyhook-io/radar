package k8s

import (
	"fmt"
	"hash/fnv"
	"log"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/logsafe"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	corev1listers "k8s.io/client-go/listers/core/v1"
)

// DetectMissingRefs scans cache for resources whose by-name references point at
// targets that don't exist. These are direct configuration errors — not
// heuristic, not benign in the cases checked here:
//
//   - Pod → PVC                                  (pod won't schedule)
//   - Pod → ServiceAccount (non-default)         (pod can't start)
//   - Pod → ConfigMap   (when not optional)      (pod fails to start)
//   - Pod → Secret      (when not optional)      (pod fails to start)
//   - Pod → imagePullSecret                      (ImagePullBackOff on private registry)
//   - StatefulSet → headless serviceName         (per-pod DNS not created, peer discovery broken)
//   - HPA → scaleTargetRef                       (HPA inert until target exists)
//   - Ingress → backend Service                  (route returns nothing)
//   - Ingress → backend service port             (proxy config breaks, traffic dropped)
//   - Ingress → TLS secretName                   (TLS falls back to default cert)
//   - PVC → StorageClass (when specified)        (PVC stays Pending)
//   - RoleBinding / ClusterRoleBinding → Role / ClusterRole (binding inert)
//
// Admission-webhook ref checks (Validating/MutatingWebhookConfiguration →
// clientConfig.service) live in DetectMissingWebhookRefs — they require the
// dynamic cache because admissionregistration.k8s.io kinds aren't in the
// typed lister set.
//
// Heuristic-tier checks (NetworkPolicy podSelector matching no pods,
// "Deployment without a Service when peers have one") are NOT included —
// they have legitimate use cases that would generate false positives.
//
// Each check uses the "we know it's missing vs we can't tell" rule: when
// the target's lister isn't available in cache (e.g., deferred informer
// hasn't been warmed yet), the check is silently skipped. This is the
// conservative path — better to under-report than to false-positive every
// ref during cold-cache windows. The trade-off: a freshly-started radar
// may miss the SA-missing case until something else triggers the
// ServiceAccount informer.
//
// namespace="" scans all namespaces for namespaced sources. Cluster-scoped
// sources (ClusterRoleBinding) are only scanned when namespace="" — passing
// a namespace narrows the result set, matching DetectProblems' semantics.
func DetectMissingRefs(cache *ResourceCache, namespace string) []Detection {
	if cache == nil {
		return nil
	}
	now := time.Now()

	var problems []Detection
	problems = append(problems, detectPodMissingRefs(cache, namespace, now)...)
	problems = append(problems, detectStatefulSetMissingService(cache, namespace, now)...)
	problems = append(problems, detectHPAMissingTarget(cache, namespace, now)...)
	problems = append(problems, detectIngressMissingBackend(cache, namespace, now)...)
	problems = append(problems, detectPVCMissingStorageClass(cache, namespace, now)...)
	problems = append(problems, detectRoleBindingMissingRole(cache, namespace, now)...)
	return problems
}

// missingRefProblem builds a critical-severity Problem rooted at the resource
// holding the dangling reference. Most dangling refs break a running thing now
// (a Pod can't mount a missing Secret, an Ingress route returns nothing), so
// critical is the default. Use missingRefProblemSev for the inert/latent
// classes (single-replica headless Service, deprecated-RBAC residue) that don't
// warrant a critical.
func missingRefProblem(kind, group, ns, name, reason, message string, age time.Duration) Detection {
	return missingRefProblemSev(kind, group, ns, name, "critical", reason, message, age)
}

// missingRefProblemSev is missingRefProblem with an explicit severity. Severity
// follows "does the gap break a running thing now?": critical (breaks now),
// warning (latent — will break when used), info (inert/cosmetic residue). Age
// and Duration fall back to the source resource's age — there's no separate
// "ref broke at" event to anchor to.
func missingRefProblemSev(kind, group, ns, name, severity, reason, message string, age time.Duration) Detection {
	return Detection{
		Kind:            kind,
		Group:           group,
		Namespace:       ns,
		Name:            name,
		Severity:        severity,
		Reason:          reason,
		Message:         message,
		Age:             FormatAge(age),
		AgeSeconds:      int64(age.Seconds()),
		Duration:        FormatAge(age),
		DurationSeconds: int64(age.Seconds()),
		Fingerprint:     missingRefFingerprint(reason, message),
	}
}

func missingRefFingerprint(reason, detail string) string {
	// Keep same-category causes distinct without raw ref names in the issue ID input.
	h := fnv.New64a()
	_, _ = h.Write([]byte(detail))
	return fmt.Sprintf("%s|%016x", reason, h.Sum64())
}

// withFix attaches the plain-English consequence (cause) and the concrete fix
// (action) to a dangling-ref Detection. message stays the precise locator (which
// field/path references what); cause is the lead the operator reads; action is
// the deterministic remediation, namespace-qualified.
func withFix(d Detection, cause, action string) Detection {
	d.Cause = cause
	d.Action = action
	return d
}

// cmRefDiag / secretRefDiag return the (reason, message, cause, action) tuple for
// a missing ConfigMap/Secret reference. `where` names the reference site
// ("volume", "envFrom", "env var"). Factored out because the same three sites
// repeat for both volumes and env, and the copy must not drift between them.
func cmRefDiag(where, name, ns string) (reason, message, cause, action string) {
	return "Missing ConfigMap",
		fmt.Sprintf("%s references ConfigMap %q which does not exist", where, name),
		fmt.Sprintf("ConfigMap %q, referenced by the pod's %s, doesn't exist — so the pod can't start.", name, where),
		fmt.Sprintf("Update the pod template (or recreate this Pod) to point the %s reference at an existing ConfigMap in namespace %q, mark the reference optional, remove it if obsolete, or create ConfigMap %q if the workload still requires it.", where, ns, name)
}

func secretRefDiag(where, name, ns string) (reason, message, cause, action string) {
	return "Missing Secret",
		fmt.Sprintf("%s references Secret %q which does not exist", where, name),
		fmt.Sprintf("Secret %q, referenced by the pod's %s, doesn't exist — so the pod can't start.", name, where),
		fmt.Sprintf("Update the pod template (or recreate this Pod) to point the %s reference at an existing Secret in namespace %q, mark the reference optional, remove it if obsolete, or create Secret %q if the workload still requires it.", where, ns, name)
}

// isTerminalPod reports whether a pod has terminally finished — Succeeded, or
// Failed without a pending restart. Such pods are not live workloads whose
// configuration you'd fix to make them start, so they're excluded from
// missing-ref detection.
func isTerminalPod(p *corev1.Pod) bool {
	return p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed
}

func detectPodMissingRefs(cache *ResourceCache, namespace string, now time.Time) []Detection {
	podLister := cache.Pods()
	if podLister == nil {
		return nil
	}
	var pods []*corev1.Pod
	if namespace != "" {
		pods, _ = podLister.Pods(namespace).List(labels.Everything())
	} else {
		pods, _ = podLister.List(labels.Everything())
	}

	cmLister := cache.ConfigMaps()
	secLister := cache.Secrets()
	pvcLister := cache.PersistentVolumeClaims()
	saLister := cache.ServiceAccounts()

	var out []Detection
	for _, p := range pods {
		// Terminal pods aren't a config error to fix: a Succeeded pod (or a
		// Failed one a Job won't retry) already ran to its end. Its referenced
		// ServiceAccount/ConfigMap/Secret may have been GC'd afterward, so
		// flagging the dangling ref as a live critical issue is the classic
		// completed-Job-pod false positive. Genuine failures still surface —
		// a Failed pod is reported via ClassifyPodHealth (SourceProblem) and a
		// failing Job via failedJobCondition.
		if isTerminalPod(p) {
			continue
		}
		age := now.Sub(p.CreationTimestamp.Time)
		seen := map[string]bool{}

		// Carry the resolved workload owner so missing-ref pod issues fold under
		// their controller — 50 pods missing the same ConfigMap is ONE workload
		// issue, not 50 pod rows. Mirrors the owner resolution on the
		// DetectProblems / scheduling pod paths.
		ownerGroup, ownerKind, ownerName := podOwnerKindName(cache, p)
		emit := func(reason, message, cause, action string) {
			pr := withFix(missingRefProblem("Pod", "", p.Namespace, p.Name, reason, message, age), cause, action)
			pr.OwnerGroup, pr.OwnerKind, pr.OwnerName = ownerGroup, ownerKind, ownerName
			out = append(out, pr)
		}

		// Volumes: persistentVolumeClaim, configMap, secret
		for _, v := range p.Spec.Volumes {
			switch {
			case v.PersistentVolumeClaim != nil:
				name := v.PersistentVolumeClaim.ClaimName
				if name == "" || seen["pvc:"+name] {
					continue
				}
				seen["pvc:"+name] = true
				if pvcLister == nil {
					continue
				}
				if _, err := pvcLister.PersistentVolumeClaims(p.Namespace).Get(name); err != nil {
					emit("Missing PVC",
						fmt.Sprintf("volume references PersistentVolumeClaim %q which does not exist", name),
						fmt.Sprintf("PersistentVolumeClaim %q doesn't exist, so the pod can't be scheduled.", name),
						fmt.Sprintf("Update the pod template (or recreate this Pod) so the volume's claimName points at an existing PVC in namespace %q, remove the volume/mount if obsolete, or create PVC %q if the pod still needs a new claim.", p.Namespace, name))
				}

			case v.ConfigMap != nil:
				name := v.ConfigMap.Name
				optional := v.ConfigMap.Optional != nil && *v.ConfigMap.Optional
				if name == "" || optional || seen["cm:"+name] {
					continue
				}
				seen["cm:"+name] = true
				if cmLister == nil {
					continue
				}
				if _, err := cmLister.ConfigMaps(p.Namespace).Get(name); err != nil {
					emit(cmRefDiag("volume", name, p.Namespace))
				}

			case v.Secret != nil:
				name := v.Secret.SecretName
				optional := v.Secret.Optional != nil && *v.Secret.Optional
				if name == "" || optional || seen["sec:"+name] {
					continue
				}
				seen["sec:"+name] = true
				if secLister == nil {
					continue
				}
				if _, err := secLister.Secrets(p.Namespace).Get(name); err != nil {
					emit(secretRefDiag("volume", name, p.Namespace))
				}
			}
		}

		// envFrom and individual env across all container slices
		containers := make([]corev1.Container, 0, len(p.Spec.Containers)+len(p.Spec.InitContainers))
		containers = append(containers, p.Spec.Containers...)
		containers = append(containers, p.Spec.InitContainers...)
		for _, c := range containers {
			for _, ef := range c.EnvFrom {
				if ef.ConfigMapRef != nil {
					name := ef.ConfigMapRef.Name
					optional := ef.ConfigMapRef.Optional != nil && *ef.ConfigMapRef.Optional
					if name == "" || optional || seen["cm:"+name] {
						continue
					}
					seen["cm:"+name] = true
					if cmLister == nil {
						continue
					}
					if _, err := cmLister.ConfigMaps(p.Namespace).Get(name); err != nil {
						emit(cmRefDiag("envFrom", name, p.Namespace))
					}
				}
				if ef.SecretRef != nil {
					name := ef.SecretRef.Name
					optional := ef.SecretRef.Optional != nil && *ef.SecretRef.Optional
					if name == "" || optional || seen["sec:"+name] {
						continue
					}
					seen["sec:"+name] = true
					if secLister == nil {
						continue
					}
					if _, err := secLister.Secrets(p.Namespace).Get(name); err != nil {
						emit(secretRefDiag("envFrom", name, p.Namespace))
					}
				}
			}
			for _, e := range c.Env {
				if e.ValueFrom == nil {
					continue
				}
				if r := e.ValueFrom.ConfigMapKeyRef; r != nil {
					name := r.Name
					optional := r.Optional != nil && *r.Optional
					if name == "" || optional || seen["cm:"+name] {
						continue
					}
					seen["cm:"+name] = true
					if cmLister == nil {
						continue
					}
					if _, err := cmLister.ConfigMaps(p.Namespace).Get(name); err != nil {
						emit(cmRefDiag("env var", name, p.Namespace))
					}
				}
				if r := e.ValueFrom.SecretKeyRef; r != nil {
					name := r.Name
					optional := r.Optional != nil && *r.Optional
					if name == "" || optional || seen["sec:"+name] {
						continue
					}
					seen["sec:"+name] = true
					if secLister == nil {
						continue
					}
					if _, err := secLister.Secrets(p.Namespace).Get(name); err != nil {
						emit(secretRefDiag("env var", name, p.Namespace))
					}
				}
			}
		}

		// ServiceAccount — skip when unspecified or "default" (auto-created
		// per-namespace by the SA controller). When the pod explicitly names
		// a non-default SA that doesn't exist, the pod cannot start at all —
		// the kubelet fails to mount the projected SA token volume.
		if sa := p.Spec.ServiceAccountName; sa != "" && sa != "default" {
			if saLister != nil {
				if _, err := saLister.ServiceAccounts(p.Namespace).Get(sa); err != nil {
					emit("Missing ServiceAccount",
						fmt.Sprintf("references ServiceAccount %q which does not exist", sa),
						fmt.Sprintf("ServiceAccount %q doesn't exist, so the pod can't start (or restart) with its expected identity/token.", sa),
						fmt.Sprintf("Update the pod template (or recreate this Pod) so spec.serviceAccountName points at an existing ServiceAccount in namespace %q, remove it to use the default ServiceAccount, or create ServiceAccount %q if the pod needs that identity.", p.Namespace, sa))
				}
			}
		}

		// imagePullSecrets — Secrets that authenticate against private
		// registries. No optional flag exists; a missing pull secret means
		// ImagePullBackOff for any container pulled from a registry that
		// needed those credentials. Pods whose images are all public would
		// still start, but the wire-shape signal is identical to the
		// always-real case, so flag uniformly.
		for _, ips := range p.Spec.ImagePullSecrets {
			name := ips.Name
			if name == "" || seen["pull:"+name] {
				continue
			}
			seen["pull:"+name] = true
			if secLister == nil {
				continue
			}
			if _, err := secLister.Secrets(p.Namespace).Get(name); err != nil {
				emit("Missing imagePullSecret",
					fmt.Sprintf("imagePullSecrets references Secret %q which does not exist", name),
					fmt.Sprintf("Pull Secret %q doesn't exist, so private-registry image pulls fail (ImagePullBackOff).", name),
					fmt.Sprintf("Update the pod template, recreate this Pod, or update the ServiceAccount to point at an existing pull Secret in namespace %q; remove the reference if images are public or obsolete, or create pull Secret %q if private-registry credentials are required.", p.Namespace, name))
			}
		}
	}
	return out
}

func detectHPAMissingTarget(cache *ResourceCache, namespace string, now time.Time) []Detection {
	hpaLister := cache.HorizontalPodAutoscalers()
	if hpaLister == nil {
		return nil
	}
	var hpas []*autoscalingv2.HorizontalPodAutoscaler
	if namespace != "" {
		hpas, _ = hpaLister.HorizontalPodAutoscalers(namespace).List(labels.Everything())
	} else {
		hpas, _ = hpaLister.List(labels.Everything())
	}

	var out []Detection
	for _, h := range hpas {
		ref := h.Spec.ScaleTargetRef
		if ref.Name == "" {
			continue
		}
		verifiable, ok := workloadExists(cache, ref.Kind, h.Namespace, ref.Name)
		if !verifiable || ok {
			continue
		}
		age := now.Sub(h.CreationTimestamp.Time)
		out = append(out, withFix(missingRefProblem("HorizontalPodAutoscaler", "autoscaling", h.Namespace, h.Name,
			"Missing scaleTargetRef",
			fmt.Sprintf("scaleTargetRef references %s %q which does not exist", ref.Kind, ref.Name),
			age),
			fmt.Sprintf("%s %q doesn't exist, so the autoscaler can't scale anything.", ref.Kind, ref.Name),
			fmt.Sprintf("Point scaleTargetRef at an existing workload in namespace %q, remove the HPA if the target is obsolete, or create %s %q if it should still exist.", h.Namespace, ref.Kind, ref.Name)))
	}
	return out
}

// workloadExists checks whether the named workload kind exists in cache.
// verifiable=false means we don't have a lister for this kind (or it's a kind
// we don't recognize as scalable) — caller should NOT flag, since "we can't
// tell" is different from "we KNOW it's missing." Conservative by design.
func workloadExists(cache *ResourceCache, kind, namespace, name string) (verifiable, ok bool) {
	switch kind {
	case "Deployment":
		l := cache.Deployments()
		if l == nil {
			return false, false
		}
		_, err := l.Deployments(namespace).Get(name)
		return true, err == nil
	case "StatefulSet":
		l := cache.StatefulSets()
		if l == nil {
			return false, false
		}
		_, err := l.StatefulSets(namespace).Get(name)
		return true, err == nil
	case "DaemonSet":
		l := cache.DaemonSets()
		if l == nil {
			return false, false
		}
		_, err := l.DaemonSets(namespace).Get(name)
		return true, err == nil
	}
	// ReplicaSet HPAs and custom scalable CRDs reach here — refuse to flag.
	return false, false
}

func detectIngressMissingBackend(cache *ResourceCache, namespace string, now time.Time) []Detection {
	ingLister := cache.Ingresses()
	if ingLister == nil {
		return nil
	}
	svcLister := cache.Services()
	if svcLister == nil {
		// Can't verify Service existence; refuse to flag.
		return nil
	}
	secLister := cache.Secrets()
	var ings []*networkingv1.Ingress
	if namespace != "" {
		ings, _ = ingLister.Ingresses(namespace).List(labels.Everything())
	} else {
		ings, _ = ingLister.List(labels.Everything())
	}

	var out []Detection
	for _, ing := range ings {
		age := now.Sub(ing.CreationTimestamp.Time)
		seenSvc := map[string]bool{}
		seenSec := map[string]bool{}

		// checkBackend verifies (a) the Service exists, and (b) the port
		// reference resolves against the Service's port list. A backend that
		// names a Service which exists but doesn't expose the named/numbered
		// port silently drops traffic just as badly as a missing Service.
		checkBackend := func(b networkingv1.IngressServiceBackend, sourcePath string) {
			if b.Name == "" {
				return
			}
			key := b.Name + "|" + b.Port.Name + "|" + fmt.Sprint(b.Port.Number)
			if seenSvc[key] {
				return
			}
			seenSvc[key] = true
			svc, err := svcLister.Services(ing.Namespace).Get(b.Name)
			if err != nil {
				out = append(out, withFix(missingRefProblem("Ingress", "networking.k8s.io", ing.Namespace, ing.Name,
					"Missing backend Service",
					fmt.Sprintf("%s references Service %q which does not exist", sourcePath, b.Name),
					age),
					fmt.Sprintf("Service %q doesn't exist, so this route serves nothing.", b.Name),
					fmt.Sprintf("Point the backend at an existing Service in namespace %q, remove the stale backend, or create Service %q if it should still receive traffic.", ing.Namespace, b.Name)))
				return
			}
			// Service exists — verify the port resolves.
			if b.Port.Name == "" && b.Port.Number == 0 {
				return
			}
			matched := false
			for _, sp := range svc.Spec.Ports {
				if b.Port.Name != "" && sp.Name == b.Port.Name {
					matched = true
					break
				}
				if b.Port.Number != 0 && sp.Port == b.Port.Number {
					matched = true
					break
				}
			}
			if !matched {
				portDesc := b.Port.Name
				if portDesc == "" {
					portDesc = fmt.Sprintf("%d", b.Port.Number)
				}
				out = append(out, withFix(missingRefProblem("Ingress", "networking.k8s.io", ing.Namespace, ing.Name,
					"Missing backend Service port",
					fmt.Sprintf("%s targets Service %q port %q which the Service does not expose", sourcePath, b.Name, portDesc),
					age),
					fmt.Sprintf("Service %q does not expose port %q, so traffic to this route is dropped.", b.Name, portDesc),
					fmt.Sprintf("Point the backend at a port Service %q already exposes in namespace %q, or add port %q to the Service's spec.ports if it should expose it.", b.Name, ing.Namespace, portDesc)))
			}
		}

		if ing.Spec.DefaultBackend != nil && ing.Spec.DefaultBackend.Service != nil {
			checkBackend(*ing.Spec.DefaultBackend.Service, "defaultBackend")
		}
		for _, rule := range ing.Spec.Rules {
			if rule.HTTP == nil {
				continue
			}
			for _, path := range rule.HTTP.Paths {
				if path.Backend.Service != nil {
					checkBackend(*path.Backend.Service, fmt.Sprintf("rule[host=%q].path[%q]", rule.Host, path.Path))
				}
			}
		}

		// TLS secrets. Severity is warning (not critical): when the named
		// Secret is missing, the Ingress controller typically falls back to
		// the default cert and TLS still terminates — just with the wrong
		// (or self-signed) certificate. Functionally degraded, not broken.
		if secLister == nil {
			continue
		}
		for _, tls := range ing.Spec.TLS {
			if tls.SecretName == "" || seenSec[tls.SecretName] {
				continue
			}
			seenSec[tls.SecretName] = true
			if _, err := secLister.Secrets(ing.Namespace).Get(tls.SecretName); err != nil {
				p := withFix(missingRefProblem("Ingress", "networking.k8s.io", ing.Namespace, ing.Name,
					"Missing TLS Secret",
					fmt.Sprintf("tls[].secretName references Secret %q which does not exist", tls.SecretName),
					age),
					fmt.Sprintf("TLS Secret %q doesn't exist, so the controller may serve its default/self-signed cert and HTTPS clients see warnings.", tls.SecretName),
					fmt.Sprintf("Point tls[].secretName at an existing kubernetes.io/tls Secret in namespace %q, remove the tls entry, or create TLS Secret %q if this host still needs TLS.", ing.Namespace, tls.SecretName))
				p.Severity = "warning"
				out = append(out, p)
			}
		}
	}
	return out
}

func detectStatefulSetMissingService(cache *ResourceCache, namespace string, now time.Time) []Detection {
	stsLister := cache.StatefulSets()
	if stsLister == nil {
		return nil
	}
	svcLister := cache.Services()
	if svcLister == nil {
		return nil
	}
	var stss []*appsv1.StatefulSet
	if namespace != "" {
		stss, _ = stsLister.StatefulSets(namespace).List(labels.Everything())
	} else {
		stss, _ = stsLister.List(labels.Everything())
	}

	var out []Detection
	for _, sts := range stss {
		// spec.serviceName names the headless Service that creates per-pod
		// DNS records. It's required by the StatefulSet API, so an empty
		// value is a different problem class (admission would normally
		// reject); only flag when it's set-and-missing.
		if sts.Spec.ServiceName == "" {
			continue
		}
		if _, err := svcLister.Services(sts.Namespace).Get(sts.Spec.ServiceName); err != nil {
			age := now.Sub(sts.CreationTimestamp.Time)
			// The headless Service only matters for multi-replica peer DNS. For
			// a single-replica StatefulSet (a controller running as a singleton)
			// there are no peers to discover, so the missing Service is inert —
			// info, not critical. Multi-replica is a real (if not urgent)
			// degradation → warning.
			replicas := int32(1)
			if sts.Spec.Replicas != nil {
				replicas = *sts.Spec.Replicas
			}
			severity := "info"
			message := fmt.Sprintf("spec.serviceName references Service %q which does not exist; single-replica StatefulSet has no peers, so per-pod DNS is inert", sts.Spec.ServiceName)
			cause := fmt.Sprintf("Headless Service %q doesn't exist. With one replica there are no peers, so per-pod DNS is inert today — but scaling up will silently break peer discovery.", sts.Spec.ServiceName)
			if replicas > 1 {
				severity = "warning"
				message = fmt.Sprintf("spec.serviceName references Service %q which does not exist (pods will schedule but per-pod DNS records won't be created; peer discovery silently broken)", sts.Spec.ServiceName)
				cause = fmt.Sprintf("Headless Service %q doesn't exist, so per-pod DNS records aren't created and peer discovery is broken across the replicas.", sts.Spec.ServiceName)
			}
			out = append(out, withFix(missingRefProblemSev("StatefulSet", "apps", sts.Namespace, sts.Name,
				severity, "Missing headless Service", message, age),
				cause,
				fmt.Sprintf("Create headless Service %q (clusterIP: None) selecting the StatefulSet's pods in namespace %q, or recreate the StatefulSet with spec.serviceName pointing at an existing headless Service (serviceName is immutable).", sts.Spec.ServiceName, sts.Namespace)))
		}
	}
	return out
}

// DetectMissingWebhookRefs scans admission-webhook configs
// (ValidatingWebhookConfiguration, MutatingWebhookConfiguration) for
// clientConfig.service refs that point at missing Services. Returned
// separately from DetectMissingRefs because admissionregistration.k8s.io
// kinds aren't in the typed lister set. Mirrors DetectCAPIProblems shape.
//
// Webhook misconfigurations are particularly worth surfacing because the
// failure mode is silent: with failurePolicy=Ignore, security/mutation
// rules are skipped without any visible cluster-level signal.
//
// CRD conversion webhook refs (spec.conversion.webhook.clientConfig.service)
// are NOT checked here. The dynamic cache strips spec.conversion via
// pkg/k8score/transform.go to avoid retaining heavy schema/caBundle data,
// so reading those refs from the cache is impossible. Would need a direct
// API list bypassing the transform — tracked as a follow-up.
func DetectMissingWebhookRefs(cache *ResourceCache, dynamicCache *DynamicResourceCache, discovery *ResourceDiscovery, namespace string) []Detection {
	if cache == nil || dynamicCache == nil || discovery == nil {
		return nil
	}
	// All three sources are cluster-scoped — emit only when scanning all
	// namespaces, same convention DetectMissingRefs uses for CRBs.
	if namespace != "" {
		return nil
	}
	svcLister := cache.Services()
	if svcLister == nil {
		return nil
	}
	now := time.Now()

	listByKind := func(kind, group string) []*unstructured.Unstructured {
		gvr, ok := discovery.GetGVRWithGroup(kind, group)
		if !ok {
			return nil
		}
		items, err := dynamicCache.List(gvr, "")
		if err != nil {
			log.Printf("[missing-refs] failed to list %s.%s: %v", kind, group, err)
			return nil
		}
		return items
	}

	emit := func(kind, group, name, source, svcNS, svcName string, age time.Duration) Detection {
		return withFix(missingRefProblem(kind, group, "", name,
			"Missing webhook backend Service",
			fmt.Sprintf("%s references Service %q in namespace %q which does not exist",
				source, svcName, svcNS),
			age),
			fmt.Sprintf("Webhook backend Service %q in namespace %q doesn't exist, so admission requests can't reach it — rules are bypassed (failurePolicy=Ignore) or admission is blocked (failurePolicy=Fail).", svcName, svcNS),
			fmt.Sprintf("Restore Service %q and its endpoints in namespace %q, or fix clientConfig.service to point at the correct healthy Service.", svcName, svcNS))
	}

	checkWebhookList := func(items []*unstructured.Unstructured, ownerKind, ownerGroup, webhookPath string) []Detection {
		var problems []Detection
		for _, item := range items {
			webhooks, found, err := unstructured.NestedSlice(item.Object, webhookPath)
			if err != nil || !found {
				continue
			}
			age := now.Sub(item.GetCreationTimestamp().Time)
			seen := map[string]bool{}
			for _, w := range webhooks {
				wm, ok := w.(map[string]any)
				if !ok {
					continue
				}
				ccSvc, found, err := unstructured.NestedMap(wm, "clientConfig", "service")
				if err != nil || !found {
					continue // URL-based clientConfig has no Service ref
				}
				svcName, _ := ccSvc["name"].(string)
				svcNS, _ := ccSvc["namespace"].(string)
				if svcName == "" || svcNS == "" {
					continue
				}
				key := svcNS + "/" + svcName
				if seen[key] {
					continue
				}
				seen[key] = true
				if _, err := svcLister.Services(svcNS).Get(svcName); err != nil {
					whName, _ := wm["name"].(string)
					source := fmt.Sprintf("webhook %q clientConfig.service", whName)
					problems = append(problems, emit(ownerKind, ownerGroup, item.GetName(), source, svcNS, svcName, age))
				}
			}
		}
		return problems
	}

	var out []Detection
	out = append(out, checkWebhookList(
		listByKind("ValidatingWebhookConfiguration", "admissionregistration.k8s.io"),
		"ValidatingWebhookConfiguration", "admissionregistration.k8s.io", "webhooks",
	)...)
	out = append(out, checkWebhookList(
		listByKind("MutatingWebhookConfiguration", "admissionregistration.k8s.io"),
		"MutatingWebhookConfiguration", "admissionregistration.k8s.io", "webhooks",
	)...)
	return out
}

// DetectMissingGatewayRefs scans Gateway API Routes for backend Service refs
// that point at missing Services or missing Service ports. Controller status
// usually reports these via ResolvedRefs=False, but this structural check still
// works before a controller reconciles and on clusters where route status is
// sparse.
func DetectMissingGatewayRefs(cache *ResourceCache, dynamicCache *DynamicResourceCache, discovery *ResourceDiscovery, namespace string) []Detection {
	if cache == nil || dynamicCache == nil || discovery == nil {
		return nil
	}
	svcLister := cache.Services()
	if svcLister == nil {
		return nil
	}
	now := time.Now()
	getReferenceGrants := gatewayReferenceGrantGetter(dynamicCache, discovery)
	var out []Detection
	for _, kind := range []string{"HTTPRoute", "GRPCRoute", "TCPRoute", "TLSRoute"} {
		gvr, ok := discovery.GetGVRWithGroup(kind, "gateway.networking.k8s.io")
		if !ok {
			continue
		}
		var routes []*unstructured.Unstructured
		if namespace != "" {
			items, err := dynamicCache.List(gvr, namespace)
			if err != nil {
				log.Printf("[missing-refs] failed to list %s.gateway.networking.k8s.io in %s: %s", logsafe.Sanitize(kind), logsafe.Sanitize(namespace), logsafe.Sanitize(err.Error()))
				continue
			}
			routes = items
		} else {
			items, err := dynamicCache.ListWatched(gvr)
			if err != nil {
				log.Printf("[missing-refs] failed to list %s.gateway.networking.k8s.io: %s", logsafe.Sanitize(kind), logsafe.Sanitize(err.Error()))
				continue
			}
			routes = items
		}
		for _, route := range routes {
			out = append(out, detectGatewayRouteMissingBackends(svcLister, getReferenceGrants, kind, route, now)...)
		}
	}
	return out
}

type referenceGrantGetter func(namespace string) ([]*unstructured.Unstructured, bool)

func gatewayReferenceGrantGetter(dynamicCache *DynamicResourceCache, discovery *ResourceDiscovery) referenceGrantGetter {
	refGrantGVR, ok := discovery.GetGVRWithGroup("ReferenceGrant", "gateway.networking.k8s.io")
	if !ok {
		return nil
	}
	grantsByNS := map[string][]*unstructured.Unstructured{}
	knownByNS := map[string]bool{}
	return func(namespace string) ([]*unstructured.Unstructured, bool) {
		if knownByNS[namespace] {
			return grantsByNS[namespace], true
		}
		items, err := dynamicCache.List(refGrantGVR, namespace)
		if err != nil {
			log.Printf("[missing-refs] failed to list ReferenceGrant.gateway.networking.k8s.io in %s: %s", logsafe.Sanitize(namespace), logsafe.Sanitize(err.Error()))
			return nil, false
		}
		grantsByNS[namespace] = items
		knownByNS[namespace] = true
		return items, true
	}
}

func detectGatewayRouteMissingBackends(svcLister corev1listers.ServiceLister, getReferenceGrants referenceGrantGetter, kind string, route *unstructured.Unstructured, now time.Time) []Detection {
	rules, found, err := unstructured.NestedSlice(route.Object, "spec", "rules")
	if err != nil || !found {
		return nil
	}
	age := now.Sub(route.GetCreationTimestamp().Time)
	seen := map[string]bool{}
	var out []Detection
	for ri, r := range rules {
		rm, ok := r.(map[string]any)
		if !ok {
			continue
		}
		refs, _ := rm["backendRefs"].([]any)
		for bi, ref := range refs {
			refm, ok := ref.(map[string]any)
			if !ok {
				continue
			}
			name, _ := refm["name"].(string)
			if name == "" || !gatewayBackendRefIsService(refm) {
				continue
			}
			svcNS := route.GetNamespace()
			if ns, _ := refm["namespace"].(string); ns != "" {
				svcNS = ns
			}
			port := gatewayBackendPort(refm)
			key := svcNS + "/" + name + "/" + port
			if seen[key] {
				continue
			}
			seen[key] = true
			svc, err := svcLister.Services(svcNS).Get(name)
			source := fmt.Sprintf("spec.rules[%d].backendRefs[%d]", ri, bi)
			if err != nil {
				out = append(out, withFix(missingRefProblem(kind, "gateway.networking.k8s.io", route.GetNamespace(), route.GetName(),
					"Missing Gateway backend Service",
					fmt.Sprintf("%s references Service %q in namespace %q which does not exist", source, name, svcNS),
					age),
					fmt.Sprintf("Service %q in namespace %q doesn't exist, so this route's backend receives no traffic.", name, svcNS),
					fmt.Sprintf("Point the backendRef at an existing Service in namespace %q, remove the stale backendRef, or create Service %q if this route should still send traffic there.", svcNS, name)))
				continue
			}
			if port == "" {
				out = append(out, withFix(missingRefProblem(kind, "gateway.networking.k8s.io", route.GetNamespace(), route.GetName(),
					"Missing Gateway backend Service port",
					fmt.Sprintf("%s references Service %q in namespace %q without specifying a port", source, name, svcNS),
					age),
					fmt.Sprintf("No port is set for Service %q, so the route backend can't receive traffic.", name),
					"Set the backendRef port to one the Service exposes."))
				continue
			}
			if !serviceHasPort(svc, port) {
				out = append(out, withFix(missingRefProblem(kind, "gateway.networking.k8s.io", route.GetNamespace(), route.GetName(),
					"Missing Gateway backend Service port",
					fmt.Sprintf("%s targets Service %q in namespace %q port %q which the Service does not expose", source, name, svcNS, port),
					age),
					fmt.Sprintf("Service %q does not expose port %q, so the route backend can't receive traffic.", name, port),
					fmt.Sprintf("Reference a port Service %q already exposes in namespace %q, or add port %q to the Service if it should expose it.", name, svcNS, port)))
			}
			if svcNS != route.GetNamespace() && getReferenceGrants != nil {
				if grants, ok := getReferenceGrants(svcNS); ok && !gatewayReferenceGranted(grants, kind, route.GetNamespace(), name) {
					out = append(out, withFix(missingRefProblem(kind, "gateway.networking.k8s.io", route.GetNamespace(), route.GetName(),
						"Missing Gateway ReferenceGrant",
						fmt.Sprintf("%s references Service %q in namespace %q, but no ReferenceGrant allows it", source, name, svcNS),
						age),
						fmt.Sprintf("Namespace %q has no ReferenceGrant allowing %s in %q to reference Service %q, so cross-namespace routing is denied.", svcNS, kind, route.GetNamespace(), name),
						fmt.Sprintf("Create a ReferenceGrant in namespace %q allowing %s from namespace %q to reference Services.", svcNS, kind, route.GetNamespace())))
				}
			}
		}
	}
	return out
}

func gatewayBackendRefIsService(ref map[string]any) bool {
	group, _ := ref["group"].(string)
	kind, _ := ref["kind"].(string)
	return group == "" && (kind == "" || kind == "Service")
}

func gatewayBackendPort(ref map[string]any) string {
	switch v := ref["port"].(type) {
	case int64:
		return fmt.Sprintf("%d", v)
	case int32:
		return fmt.Sprintf("%d", v)
	case int:
		return fmt.Sprintf("%d", v)
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
	case string:
		return v
	}
	return ""
}

func serviceHasPort(svc *corev1.Service, port string) bool {
	for _, sp := range svc.Spec.Ports {
		if sp.Name == port || fmt.Sprintf("%d", sp.Port) == port {
			return true
		}
	}
	return false
}

func gatewayReferenceGranted(grants []*unstructured.Unstructured, routeKind, routeNS, svcName string) bool {
	for _, grant := range grants {
		froms, foundFrom, _ := unstructured.NestedSlice(grant.Object, "spec", "from")
		tos, foundTo, _ := unstructured.NestedSlice(grant.Object, "spec", "to")
		if !foundFrom || !foundTo {
			continue
		}
		if !referenceGrantAllowsFrom(froms, routeKind, routeNS) {
			continue
		}
		if referenceGrantAllowsToService(tos, svcName) {
			return true
		}
	}
	return false
}

func referenceGrantAllowsFrom(froms []any, routeKind, routeNS string) bool {
	for _, from := range froms {
		fm, ok := from.(map[string]any)
		if !ok {
			continue
		}
		group, _ := fm["group"].(string)
		kind, _ := fm["kind"].(string)
		namespace, _ := fm["namespace"].(string)
		if group == "" {
			group = "gateway.networking.k8s.io"
		}
		if group == "gateway.networking.k8s.io" && kind == routeKind && namespace == routeNS {
			return true
		}
	}
	return false
}

func referenceGrantAllowsToService(tos []any, svcName string) bool {
	for _, to := range tos {
		tm, ok := to.(map[string]any)
		if !ok {
			continue
		}
		group, _ := tm["group"].(string)
		kind, _ := tm["kind"].(string)
		name, _ := tm["name"].(string)
		if group == "" && kind == "Service" && (name == "" || name == svcName) {
			return true
		}
	}
	return false
}

func detectPVCMissingStorageClass(cache *ResourceCache, namespace string, now time.Time) []Detection {
	pvcLister := cache.PersistentVolumeClaims()
	if pvcLister == nil {
		return nil
	}
	scLister := cache.StorageClasses()
	if scLister == nil {
		// Can't verify StorageClass existence; refuse to flag.
		return nil
	}
	var pvcs []*corev1.PersistentVolumeClaim
	if namespace != "" {
		pvcs, _ = pvcLister.PersistentVolumeClaims(namespace).List(labels.Everything())
	} else {
		pvcs, _ = pvcLister.List(labels.Everything())
	}

	var out []Detection
	for _, pvc := range pvcs {
		// nil or empty storageClassName defers to the cluster default — that's
		// not a ref error. Only flag when a concrete name is set + missing.
		if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName == "" {
			continue
		}
		scName := *pvc.Spec.StorageClassName
		if _, err := scLister.Get(scName); err != nil {
			age := now.Sub(pvc.CreationTimestamp.Time)
			det := withFix(missingRefProblem("PersistentVolumeClaim", "", pvc.Namespace, pvc.Name,
				"Missing StorageClass",
				fmt.Sprintf("references StorageClass %q which does not exist", scName),
				age),
				fmt.Sprintf("StorageClass %q doesn't exist, so the PVC stays Pending and no volume is provisioned.", scName),
				fmt.Sprintf("Recreate the PVC with spec.storageClassName set to an existing StorageClass or unset to use the cluster default (storageClassName is immutable), remove the stale claim if obsolete, or create StorageClass %q if that class should exist.", scName))
			// Same invariant as the phase detector: a Pending PVC has never
			// been bound, so the missing class has been breaking it since
			// creation. Stamp timing here because this row wins the dedupe
			// against the phase row (it names the cause) and must not lose
			// the timing the phase row carried.
			if pvc.Status.Phase == corev1.ClaimPending {
				det.IssueTiming = "started_at_resource_creation"
				det.IssueTimingBasis = "phase"
			}
			out = append(out, det)
		}
	}
	return out
}

// danglingRoleBindingSeverity rates a binding whose roleRef target is missing.
// A dangling binding grants no permissions, so it's never critical — at most a
// latent footgun (warning). Deprecated-PodSecurityPolicy residue (GKE's
// gce:podsecuritypolicy:* bindings, left behind after PSP was removed in k8s
// 1.25) is inert managed cruft present on every GKE cluster → info.
func danglingRoleBindingSeverity(bindingName, roleRefName string) string {
	if strings.HasPrefix(bindingName, "gce:podsecuritypolicy:") || strings.HasPrefix(roleRefName, "gce:podsecuritypolicy:") {
		return "info"
	}
	return "warning"
}

func detectRoleBindingMissingRole(cache *ResourceCache, namespace string, now time.Time) []Detection {
	roleLister := cache.Roles()
	crLister := cache.ClusterRoles()
	rbLister := cache.RoleBindings()
	crbLister := cache.ClusterRoleBindings()

	roleExists := func(kind, ns, name string) (verifiable, ok bool) {
		switch kind {
		case "Role":
			if roleLister == nil {
				return false, false
			}
			_, err := roleLister.Roles(ns).Get(name)
			return true, err == nil
		case "ClusterRole":
			if crLister == nil {
				return false, false
			}
			_, err := crLister.Get(name)
			return true, err == nil
		}
		return false, false
	}

	var out []Detection

	if rbLister != nil {
		var rbs []*rbacv1.RoleBinding
		if namespace != "" {
			rbs, _ = rbLister.RoleBindings(namespace).List(labels.Everything())
		} else {
			rbs, _ = rbLister.List(labels.Everything())
		}
		for _, rb := range rbs {
			verifiable, ok := roleExists(rb.RoleRef.Kind, rb.Namespace, rb.RoleRef.Name)
			if !verifiable || ok {
				continue
			}
			age := now.Sub(rb.CreationTimestamp.Time)
			out = append(out, withFix(missingRefProblemSev("RoleBinding", "rbac.authorization.k8s.io", rb.Namespace, rb.Name,
				danglingRoleBindingSeverity(rb.Name, rb.RoleRef.Name), "Missing roleRef target",
				fmt.Sprintf("roleRef points at %s %q which does not exist", rb.RoleRef.Kind, rb.RoleRef.Name),
				age),
				fmt.Sprintf("%s %q doesn't exist, so this RoleBinding grants no permissions.", rb.RoleRef.Kind, rb.RoleRef.Name),
				fmt.Sprintf("Recreate the binding pointing roleRef at an existing role (roleRef is immutable), or create %s %q if that role should exist.", rb.RoleRef.Kind, rb.RoleRef.Name)))
		}
	}

	// ClusterRoleBindings are cluster-scoped. Only emit when namespace is
	// unset — matches DetectProblems' convention for cluster-scoped rows
	// (e.g. Node problems are only included when scanning all namespaces).
	if crbLister != nil && namespace == "" {
		crbs, _ := crbLister.List(labels.Everything())
		for _, crb := range crbs {
			verifiable, ok := roleExists(crb.RoleRef.Kind, "", crb.RoleRef.Name)
			if !verifiable || ok {
				continue
			}
			age := now.Sub(crb.CreationTimestamp.Time)
			out = append(out, withFix(missingRefProblemSev("ClusterRoleBinding", "rbac.authorization.k8s.io", "", crb.Name,
				danglingRoleBindingSeverity(crb.Name, crb.RoleRef.Name), "Missing roleRef target",
				fmt.Sprintf("roleRef points at %s %q which does not exist", crb.RoleRef.Kind, crb.RoleRef.Name),
				age),
				fmt.Sprintf("%s %q doesn't exist, so this ClusterRoleBinding grants no permissions.", crb.RoleRef.Kind, crb.RoleRef.Name),
				fmt.Sprintf("Recreate the binding pointing roleRef at an existing role (roleRef is immutable), or create %s %q if that role should exist.", crb.RoleRef.Kind, crb.RoleRef.Name)))
		}
	}
	return out
}
