package k8s

import (
	"fmt"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
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
//   - HPA → scaleTargetRef                       (HPA inert until target exists)
//   - Ingress → backend Service                  (route returns nothing)
//   - PVC → StorageClass (when specified)        (PVC stays Pending)
//   - RoleBinding / ClusterRoleBinding → Role / ClusterRole (binding inert)
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
	var ings []*networkingv1.Ingress
	if namespace != "" {
		ings, _ = ingLister.Ingresses(namespace).List(labels.Everything())
	} else {
		ings, _ = ingLister.List(labels.Everything())
	}

	var out []Problem
	for _, ing := range ings {
		age := now.Sub(ing.CreationTimestamp.Time)
		seen := map[string]bool{}

		check := func(svcName string, sourcePath string) {
			if svcName == "" || seen[svcName] {
				return
			}
			seen[svcName] = true
			if _, err := svcLister.Services(ing.Namespace).Get(svcName); err != nil {
				out = append(out, missingRefProblem("Ingress", "networking.k8s.io", ing.Namespace, ing.Name,
					"Missing backend Service",
					fmt.Sprintf("%s references Service %q which does not exist (route returns nothing)", sourcePath, svcName),
					age))
			}
		}

		if ing.Spec.DefaultBackend != nil && ing.Spec.DefaultBackend.Service != nil {
			check(ing.Spec.DefaultBackend.Service.Name, "defaultBackend")
		}
		for _, rule := range ing.Spec.Rules {
			if rule.HTTP == nil {
				continue
			}
			for _, path := range rule.HTTP.Paths {
				if path.Backend.Service != nil {
					check(path.Backend.Service.Name, fmt.Sprintf("rule[host=%q].path[%q]", rule.Host, path.Path))
				}
			}
		}
	}
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
