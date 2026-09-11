package opencost

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/k8s"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	pkgopencost "github.com/skyhook-io/radar/pkg/opencost"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	appslisters "k8s.io/client-go/listers/apps/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
)

const currencyDetectionTTL = 30 * time.Second

type CurrencyResolver struct {
	mu             sync.Mutex
	override       string
	cached         string
	cachedDetected bool
	expiresAt      time.Time
}

func NewCurrencyResolver(override string) *CurrencyResolver {
	return &CurrencyResolver{override: override}
}

func (r *CurrencyResolver) Resolve() string {
	var cache currencyCache
	if resourceCache := k8s.GetResourceCache(); resourceCache != nil {
		cache = resourceCache
	}
	return r.resolve(cache, clusterCurrencyDetectionAllowed(), time.Now())
}

func (r *CurrencyResolver) resolve(cache currencyCache, detectionAllowed bool, now time.Time) string {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.override != "" {
		return r.override
	}
	if !detectionAllowed {
		return pkgopencost.DefaultCurrency
	}
	if now.Before(r.expiresAt) {
		return r.cached
	}

	detection := detectOpenCostCurrencyState(cache)
	if !detection.inputsAvailable {
		return pkgopencost.DefaultCurrency
	}
	if detection.currency != "" {
		r.cached = detection.currency
		r.cachedDetected = true
	} else if detection.hasActiveWorkload || !r.cachedDetected {
		r.cached = pkgopencost.DefaultCurrency
		r.cachedDetected = false
	}
	r.expiresAt = now.Add(currencyDetectionTTL)
	return r.cached
}

func (r *CurrencyResolver) SetOverride(override string) {
	r.mu.Lock()
	r.override = override
	r.cached = ""
	r.cachedDetected = false
	r.expiresAt = time.Time{}
	r.mu.Unlock()
}

func (r *CurrencyResolver) Invalidate() {
	r.mu.Lock()
	r.cached = ""
	r.cachedDetected = false
	r.expiresAt = time.Time{}
	r.mu.Unlock()
}

func clusterCurrencyDetectionAllowed() bool {
	if ConfigSnapshot().Source == SourceKubecost || SelectedSourceSnapshot() == SourceKubecost {
		return true
	}
	client := prometheuspkg.GetClient()
	return client == nil || !client.HasManualURL()
}

type currencyCache interface {
	Deployments() appslisters.DeploymentLister
	StatefulSets() appslisters.StatefulSetLister
	ConfigMaps() corelisters.ConfigMapLister
}

func detectOpenCostCurrency(cache currencyCache) string {
	return detectOpenCostCurrencyState(cache).currency
}

type currencyDetection struct {
	currency          string
	hasActiveWorkload bool
	inputsAvailable   bool
}

func detectOpenCostCurrencyState(cache currencyCache) currencyDetection {
	if cache == nil {
		return currencyDetection{}
	}
	configMapLister := cache.ConfigMaps()
	deploymentLister := cache.Deployments()
	statefulSetLister := cache.StatefulSets()
	if deploymentLister == nil && statefulSetLister == nil {
		return currencyDetection{}
	}

	var deployments []*appsv1.Deployment
	if deploymentLister != nil {
		var err error
		deployments, err = deploymentLister.List(labels.Everything())
		if err != nil {
			return currencyDetection{}
		}
	}
	var statefulSets []*appsv1.StatefulSet
	if statefulSetLister != nil {
		var err error
		statefulSets, err = statefulSetLister.List(labels.Everything())
		if err != nil {
			return currencyDetection{}
		}
	}

	detection := currencyDetection{inputsAvailable: true}
	var displayCurrency, configMapCurrency string
	var displayCurrencyDeclared, displayCurrencyAmbiguous, configMapCurrencyAmbiguous bool
	considerCurrency := func(candidate *string, ambiguous *bool, code string) {
		if code == "" {
			return
		}
		if *candidate != "" && *candidate != code {
			*candidate = ""
			*ambiguous = true
			return
		}
		if !*ambiguous {
			*candidate = code
		}
	}
	considerWorkload := func(namespace, name string, objectLabels map[string]string, containers []corev1.Container, availableReplicas, readyReplicas int32, replicas *int32) {
		if (availableReplicas == 0 && readyReplicas == 0) || (replicas != nil && *replicas == 0) {
			return
		}
		if !isOpenCostWorkload(containers) {
			return
		}
		detection.hasActiveWorkload = true

		if isKubecostWorkload(name, objectLabels, containers) {
			workloadDisplayCurrencyDeclared := false
			for _, container := range containers {
				for _, env := range container.Env {
					if env.Name != "DISPLAY_CURRENCY" {
						continue
					}
					if env.ValueFrom == nil && strings.TrimSpace(env.Value) == "" {
						continue
					}
					displayCurrencyDeclared = true
					workloadDisplayCurrencyDeclared = true
					if env.ValueFrom != nil {
						displayCurrencyAmbiguous = true
						continue
					}
					code := normalizedDetectedCurrency(env.Value)
					if code == "" {
						displayCurrencyAmbiguous = true
						continue
					}
					considerCurrency(&displayCurrency, &displayCurrencyAmbiguous, code)
				}
			}
			if workloadDisplayCurrencyDeclared {
				return
			}
		}

		configMapNames := map[string]bool{}
		for _, container := range containers {
			for _, env := range container.Env {
				if env.Name == "PRICING_CONFIGMAP_NAME" && strings.TrimSpace(env.Value) != "" {
					configMapNames[strings.TrimSpace(env.Value)] = true
				}
			}
		}
		if len(configMapNames) == 0 {
			configMapNames["custom-pricing-model"] = true
			configMapNames["pricing-configs"] = true
		}
		if configMapLister == nil {
			return
		}
		for name := range configMapNames {
			configMap, getErr := configMapLister.ConfigMaps(namespace).Get(name)
			if getErr != nil {
				continue
			}
			code, ambiguous := currencyFromConfigMap(configMap.Data)
			if ambiguous {
				configMapCurrencyAmbiguous = true
			}
			considerCurrency(&configMapCurrency, &configMapCurrencyAmbiguous, code)
		}
	}

	for _, deployment := range deployments {
		considerWorkload(
			deployment.Namespace,
			deployment.Name,
			deployment.Labels,
			deployment.Spec.Template.Spec.Containers,
			deployment.Status.AvailableReplicas,
			deployment.Status.ReadyReplicas,
			deployment.Spec.Replicas,
		)
	}
	for _, statefulSet := range statefulSets {
		considerWorkload(
			statefulSet.Namespace,
			statefulSet.Name,
			statefulSet.Labels,
			statefulSet.Spec.Template.Spec.Containers,
			statefulSet.Status.AvailableReplicas,
			statefulSet.Status.ReadyReplicas,
			statefulSet.Spec.Replicas,
		)
	}
	if !displayCurrencyDeclared && configMapLister == nil {
		return currencyDetection{}
	}

	if displayCurrencyDeclared {
		if !displayCurrencyAmbiguous {
			detection.currency = displayCurrency
		}
	} else if !configMapCurrencyAmbiguous {
		detection.currency = configMapCurrency
	}
	return detection
}

func isOpenCostWorkload(containers []corev1.Container) bool {
	return containerIdentityContains(containers, "opencost", "cost-model", "cost-analyzer")
}

func isKubecostWorkload(name string, objectLabels map[string]string, containers []corev1.Container) bool {
	if containerIdentityContains(containers, "kubecost", "cost-analyzer") {
		return true
	}
	return containerIdentityContains(containers, "cost-model") && objectIdentityContains(name, objectLabels, "kubecost", "cost-analyzer")
}

func objectIdentityContains(name string, objectLabels map[string]string, values ...string) bool {
	identities := []string{name}
	for _, key := range []string{"app", "name", "component", "app.kubernetes.io/name", "app.kubernetes.io/instance", "app.kubernetes.io/component"} {
		identities = append(identities, objectLabels[key])
	}
	return identityContains(identities, values...)
}

func containerIdentityContains(containers []corev1.Container, values ...string) bool {
	identities := make([]string, 0, len(containers)*2)
	for _, container := range containers {
		identities = append(identities, container.Name, container.Image)
	}
	return identityContains(identities, values...)
}

func identityContains(identities []string, values ...string) bool {
	for _, identity := range identities {
		identity = strings.ToLower(identity)
		for _, value := range values {
			if strings.Contains(identity, value) {
				return true
			}
		}
	}
	return false
}

func currencyFromConfigMap(data map[string]string) (string, bool) {
	detected := ""
	consider := func(value string) bool {
		code := normalizedDetectedCurrency(value)
		if code == "" {
			return true
		}
		if detected != "" && detected != code {
			return false
		}
		detected = code
		return true
	}
	for key, value := range data {
		if strings.EqualFold(key, "currencyCode") {
			if !consider(value) {
				return "", true
			}
		}
	}
	for _, value := range data {
		var pricing struct {
			CurrencyCode string `json:"currencyCode"`
		}
		if json.Unmarshal([]byte(value), &pricing) == nil && pricing.CurrencyCode != "" {
			if !consider(pricing.CurrencyCode) {
				return "", true
			}
		}
	}
	return detected, false
}

func normalizedDetectedCurrency(value string) string {
	code, err := config.NormalizeOpenCostCurrency(value)
	if err != nil {
		return ""
	}
	return code
}
