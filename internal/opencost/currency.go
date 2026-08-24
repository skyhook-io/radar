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
	if detection.currency != "" {
		r.cached = detection.currency
		r.cachedDetected = true
	} else if detection.hasActiveDeployment || !r.cachedDetected {
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
	client := prometheuspkg.GetClient()
	return client == nil || !client.HasManualURL()
}

type currencyCache interface {
	Deployments() appslisters.DeploymentLister
	ConfigMaps() corelisters.ConfigMapLister
}

func detectOpenCostCurrency(cache currencyCache) string {
	return detectOpenCostCurrencyState(cache).currency
}

type currencyDetection struct {
	currency            string
	hasActiveDeployment bool
}

func detectOpenCostCurrencyState(cache currencyCache) currencyDetection {
	if cache == nil || cache.Deployments() == nil || cache.ConfigMaps() == nil {
		return currencyDetection{}
	}
	deployments, err := cache.Deployments().List(labels.Everything())
	if err != nil {
		return currencyDetection{}
	}

	detection := currencyDetection{}
	for _, deployment := range deployments {
		if deployment.Status.AvailableReplicas == 0 ||
			(deployment.Spec.Replicas != nil && *deployment.Spec.Replicas == 0) {
			continue
		}
		if !isOpenCostDeployment(deployment.Name, deployment.Labels, deployment.Spec.Template.Spec.Containers) {
			continue
		}
		detection.hasActiveDeployment = true
		configMapNames := map[string]bool{}
		for _, container := range deployment.Spec.Template.Spec.Containers {
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
		for name := range configMapNames {
			configMap, getErr := cache.ConfigMaps().ConfigMaps(deployment.Namespace).Get(name)
			if getErr != nil {
				continue
			}
			code := currencyFromConfigMap(configMap.Data)
			if code == "" {
				continue
			}
			if detection.currency != "" && detection.currency != code {
				detection.currency = ""
				return detection
			}
			detection.currency = code
		}
	}
	return detection
}

func isOpenCostDeployment(name string, objectLabels map[string]string, containers []corev1.Container) bool {
	identities := []string{name}
	for _, key := range []string{"app", "name", "component", "app.kubernetes.io/name", "app.kubernetes.io/instance", "app.kubernetes.io/component"} {
		identities = append(identities, objectLabels[key])
	}
	for _, container := range containers {
		identities = append(identities, container.Name, container.Image)
	}
	for _, identity := range identities {
		identity = strings.ToLower(identity)
		if strings.Contains(identity, "opencost") || strings.Contains(identity, "kubecost") ||
			strings.Contains(identity, "cost-model") || strings.Contains(identity, "cost-analyzer") {
			return true
		}
	}
	return false
}

func currencyFromConfigMap(data map[string]string) string {
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
				return ""
			}
		}
	}
	for _, value := range data {
		var pricing struct {
			CurrencyCode string `json:"currencyCode"`
		}
		if json.Unmarshal([]byte(value), &pricing) == nil && pricing.CurrencyCode != "" {
			if !consider(pricing.CurrencyCode) {
				return ""
			}
		}
	}
	return detected
}

func normalizedDetectedCurrency(value string) string {
	code, err := config.NormalizeOpenCostCurrency(value)
	if err != nil {
		return ""
	}
	return code
}
