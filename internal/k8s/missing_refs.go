package k8s

import (
	"fmt"
	"log"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
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
func DetectMissingRefs(cache *ResourceCache, namespace string) []Problem {
	if cache == nil {
		return nil
	}
	now := time.Now()

	var problems []Problem
	problems = append(problems, detectPodMissingRefs(cache, namespace, now)...)
	problems = append(problems, detectStatefulSetMissingService(cache, namespace, now)...)
	problems = append(problems, detectHPAMissingTarget(cache, namespace, now)...)
	problems = append(problems, detectIngressMissingBackend(cache, namespace, now)...)
	problems = append(problems, detectPVCMissingStorageClass(cache, namespace, now)...)
	problems = append(problems, detectRoleBindingMissingRole(cache, namespace, now)...)
	return problems
}

// missingRefProblem builds a critical-severity Problem rooted at the resource
// holding the dangling reference. Age and Duration both fall back to the
// source resource's age — there's no separate "ref broke at" event we can
// anchor to, and any other duration would be a heuristic.
func missingRefProblem(kind, group, ns, name, reason, message string, age time.Duration) Problem {
	return Problem{
		Kind:            kind,
		Group:           group,
		Namespace:       ns,
		Name:            name,
		Severity:        "critical",
		Reason:          reason,
		Message:         message,
		Age:             FormatAge(age),
		AgeSeconds:      int64(age.Seconds()),
		Duration:        FormatAge(age),
		DurationSeconds: int64(age.Seconds()),
	}
}

func detectPodMissingRefs(cache *ResourceCache, namespace string, now time.Time) []Problem {
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

	var out []Problem
	for _, p := range pods {
		age := now.Sub(p.CreationTimestamp.Time)
		seen := map[string]bool{}

		emit := func(reason, message string) {
			out = append(out, missingRefProblem("Pod", "", p.Namespace, p.Name, reason, message, age))
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
						fmt.Sprintf("references PersistentVolumeClaim %q which does not exist (pod will not schedule)", name))
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
					emit("Missing ConfigMap",
						fmt.Sprintf("volume references ConfigMap %q which does not exist (ref not marked optional)", name))
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
					emit("Missing Secret",
						fmt.Sprintf("volume references Secret %q which does not exist (ref not marked optional)", name))
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
						emit("Missing ConfigMap",
							fmt.Sprintf("envFrom references ConfigMap %q which does not exist (ref not marked optional)", name))
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
						emit("Missing Secret",
							fmt.Sprintf("envFrom references Secret %q which does not exist (ref not marked optional)", name))
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
						emit("Missing ConfigMap",
							fmt.Sprintf("env var references ConfigMap %q which does not exist (ref not marked optional)", name))
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
						emit("Missing Secret",
							fmt.Sprintf("env var references Secret %q which does not exist (ref not marked optional)", name))
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
						fmt.Sprintf("references ServiceAccount %q which does not exist (default SA is not used when one is specified)", sa))
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
					fmt.Sprintf("references Secret %q which does not exist (private-registry pulls will fail with ImagePullBackOff)", name))
			}
		}
	}
	return out
}

func detectHPAMissingTarget(cache *ResourceCache, namespace string, now time.Time) []Problem {
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

	var out []Problem
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
		out = append(out, missingRefProblem("HorizontalPodAutoscaler", "autoscaling", h.Namespace, h.Name,
			"Missing scaleTargetRef",
			fmt.Sprintf("references %s %q which does not exist (HPA is inert until target appears)", ref.Kind, ref.Name),
			age))
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

func detectIngressMissingBackend(cache *ResourceCache, namespace string, now time.Time) []Problem {
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

	var out []Problem
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
				out = append(out, missingRefProblem("Ingress", "networking.k8s.io", ing.Namespace, ing.Name,
					"Missing backend Service",
					fmt.Sprintf("%s references Service %q which does not exist (route returns nothing)", sourcePath, b.Name),
					age))
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
				out = append(out, missingRefProblem("Ingress", "networking.k8s.io", ing.Namespace, ing.Name,
					"Missing backend Service port",
					fmt.Sprintf("%s targets Service %q port %q which does not exist on the Service (reverse-proxy config breaks; traffic dropped)", sourcePath, b.Name, portDesc),
					age))
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
				p := missingRefProblem("Ingress", "networking.k8s.io", ing.Namespace, ing.Name,
					"Missing TLS Secret",
					fmt.Sprintf("tls[].secretName references Secret %q which does not exist (controller will fall back to default cert; HTTPS clients will see cert warnings)", tls.SecretName),
					age)
				p.Severity = "warning"
				out = append(out, p)
			}
		}
	}
	return out
}

func detectStatefulSetMissingService(cache *ResourceCache, namespace string, now time.Time) []Problem {
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

	var out []Problem
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
			out = append(out, missingRefProblem("StatefulSet", "apps", sts.Namespace, sts.Name,
				"Missing headless Service",
				fmt.Sprintf("spec.serviceName references Service %q which does not exist (pods will schedule but per-pod DNS records won't be created; peer discovery silently broken)", sts.Spec.ServiceName),
				age))
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
func DetectMissingWebhookRefs(cache *ResourceCache, dynamicCache *DynamicResourceCache, discovery *ResourceDiscovery, namespace string) []Problem {
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

	emit := func(kind, group, name, source, svcNS, svcName string, age time.Duration) Problem {
		return missingRefProblem(kind, group, "", name,
			"Missing webhook backend Service",
			fmt.Sprintf("%s references Service %q in namespace %q which does not exist (webhook will not be invoked; admission rules silently bypassed when failurePolicy=Ignore, or admission halted when failurePolicy=Fail)",
				source, svcName, svcNS),
			age)
	}

	checkWebhookList := func(items []*unstructured.Unstructured, ownerKind, ownerGroup, webhookPath string) []Problem {
		var problems []Problem
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

	var out []Problem
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

func detectPVCMissingStorageClass(cache *ResourceCache, namespace string, now time.Time) []Problem {
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

	var out []Problem
	for _, pvc := range pvcs {
		// nil or empty storageClassName defers to the cluster default — that's
		// not a ref error. Only flag when a concrete name is set + missing.
		if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName == "" {
			continue
		}
		scName := *pvc.Spec.StorageClassName
		if _, err := scLister.Get(scName); err != nil {
			age := now.Sub(pvc.CreationTimestamp.Time)
			out = append(out, missingRefProblem("PersistentVolumeClaim", "", pvc.Namespace, pvc.Name,
				"Missing StorageClass",
				fmt.Sprintf("references StorageClass %q which does not exist (PVC will stay Pending)", scName),
				age))
		}
	}
	return out
}

func detectRoleBindingMissingRole(cache *ResourceCache, namespace string, now time.Time) []Problem {
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

	var out []Problem

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
			out = append(out, missingRefProblem("RoleBinding", "rbac.authorization.k8s.io", rb.Namespace, rb.Name,
				"Missing roleRef target",
				fmt.Sprintf("roleRef points at %s %q which does not exist (binding grants no permissions)", rb.RoleRef.Kind, rb.RoleRef.Name),
				age))
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
			out = append(out, missingRefProblem("ClusterRoleBinding", "rbac.authorization.k8s.io", "", crb.Name,
				"Missing roleRef target",
				fmt.Sprintf("roleRef points at %s %q which does not exist (binding grants no permissions)", crb.RoleRef.Kind, crb.RoleRef.Name),
				age))
		}
	}
	return out
}
