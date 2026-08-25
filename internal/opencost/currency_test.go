package opencost

import (
	"testing"
	"time"

	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	pkgopencost "github.com/skyhook-io/radar/pkg/opencost"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	appslisters "k8s.io/client-go/listers/apps/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

type testCurrencyCache struct {
	deployments appslisters.DeploymentLister
	configMaps  corelisters.ConfigMapLister
}

func (c testCurrencyCache) Deployments() appslisters.DeploymentLister { return c.deployments }
func (c testCurrencyCache) ConfigMaps() corelisters.ConfigMapLister   { return c.configMaps }

func newTestCurrencyCache(t *testing.T, objects ...any) testCurrencyCache {
	t.Helper()
	deploymentIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	configMapIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	for _, object := range objects {
		var err error
		switch value := object.(type) {
		case *appsv1.Deployment:
			err = deploymentIndexer.Add(value)
		case *corev1.ConfigMap:
			err = configMapIndexer.Add(value)
		default:
			t.Fatalf("unsupported object %T", object)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	return testCurrencyCache{
		deployments: appslisters.NewDeploymentLister(deploymentIndexer),
		configMaps:  corelisters.NewConfigMapLister(configMapIndexer),
	}
}

func openCostDeployment(namespace, name, configMapName string) *appsv1.Deployment {
	env := []corev1.EnvVar{}
	if configMapName != "" {
		env = append(env, corev1.EnvVar{Name: "PRICING_CONFIGMAP_NAME", Value: configMapName})
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "cost-model", Image: "ghcr.io/opencost/opencost:latest", Env: env},
		}}}},
		Status: appsv1.DeploymentStatus{AvailableReplicas: 1},
	}
}

func pricingConfigMap(namespace, name string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}, Data: data}
}

func TestDetectOpenCostCurrency(t *testing.T) {
	tests := []struct {
		name    string
		objects []any
		want    string
	}{
		{name: "no cache", want: ""},
		{
			name: "official chart config map",
			objects: []any{
				openCostDeployment("opencost", "opencost", "custom-pricing-model"),
				pricingConfigMap("opencost", "custom-pricing-model", map[string]string{"currencyCode": " eur ", "CPU": "0.03"}),
			},
			want: "EUR",
		},
		{
			name: "current currency missing from CLDR",
			objects: []any{
				openCostDeployment("opencost", "opencost", "custom-pricing-model"),
				pricingConfigMap("opencost", "custom-pricing-model", map[string]string{"currencyCode": "ves"}),
			},
			want: "VES",
		},
		{
			name: "custom referenced config map with json",
			objects: []any{
				openCostDeployment("finops", "cost-analyzer", "company-pricing"),
				pricingConfigMap("finops", "company-pricing", map[string]string{"default.json": `{"currencyCode":"gbp","CPU":"0.03"}`}),
			},
			want: "GBP",
		},
		{
			name: "referenced config map takes precedence over stale default",
			objects: []any{
				openCostDeployment("finops", "opencost", "company-pricing"),
				pricingConfigMap("finops", "company-pricing", map[string]string{"currencyCode": "GBP"}),
				pricingConfigMap("finops", "custom-pricing-model", map[string]string{"currencyCode": "USD"}),
			},
			want: "GBP",
		},
		{
			name: "unrelated config map ignored",
			objects: []any{
				pricingConfigMap("app", "custom-pricing-model", map[string]string{"currencyCode": "JPY"}),
			},
			want: "",
		},
		{
			name: "invalid currency ignored",
			objects: []any{
				openCostDeployment("opencost", "opencost", "custom-pricing-model"),
				pricingConfigMap("opencost", "custom-pricing-model", map[string]string{"currencyCode": "EURO"}),
			},
			want: "",
		},
		{
			name: "conflicting active installations are ambiguous",
			objects: []any{
				openCostDeployment("one", "opencost", "custom-pricing-model"),
				pricingConfigMap("one", "custom-pricing-model", map[string]string{"currencyCode": "EUR"}),
				openCostDeployment("two", "kubecost", "pricing-configs"),
				pricingConfigMap("two", "pricing-configs", map[string]string{"currencyCode": "JPY"}),
			},
			want: "",
		},
		{
			name: "inactive installation is ignored",
			objects: []any{
				func() *appsv1.Deployment {
					deployment := openCostDeployment("old", "kubecost", "pricing-configs")
					deployment.Status.AvailableReplicas = 0
					return deployment
				}(),
				pricingConfigMap("old", "pricing-configs", map[string]string{"currencyCode": "JPY"}),
				openCostDeployment("live", "opencost", "custom-pricing-model"),
				pricingConfigMap("live", "custom-pricing-model", map[string]string{"currencyCode": "EUR"}),
			},
			want: "EUR",
		},
		{
			name: "conflicting values within one config map are ambiguous",
			objects: []any{
				openCostDeployment("opencost", "opencost", "custom-pricing-model"),
				pricingConfigMap("opencost", "custom-pricing-model", map[string]string{
					"aws.json": `{"currencyCode":"USD"}`,
					"gcp.json": `{"currencyCode":"EUR"}`,
				}),
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.objects == nil {
				if got := detectOpenCostCurrency(nil); got != tt.want {
					t.Fatalf("detectOpenCostCurrency(nil) = %q, want %q", got, tt.want)
				}
				return
			}
			if got := detectOpenCostCurrency(newTestCurrencyCache(t, tt.objects...)); got != tt.want {
				t.Errorf("detectOpenCostCurrency() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCurrencyResolverOverride(t *testing.T) {
	resolver := NewCurrencyResolver("GBP")
	if got := resolver.Resolve(); got != "GBP" {
		t.Fatalf("Resolve() = %q, want GBP", got)
	}
	resolver.SetOverride("JPY")
	if got := resolver.Resolve(); got != "JPY" {
		t.Fatalf("Resolve() after SetOverride = %q, want JPY", got)
	}
	resolver.SetOverride("")
	if got := resolver.Resolve(); got != pkgopencost.DefaultCurrency {
		t.Fatalf("Resolve() after clearing override = %q, want %s", got, pkgopencost.DefaultCurrency)
	}
}

func TestCurrencyResolverRetainsDetectedCurrencyWhileDeploymentIsUnavailable(t *testing.T) {
	now := time.Now()
	resolver := NewCurrencyResolver("")
	active := newTestCurrencyCache(t,
		openCostDeployment("opencost", "opencost", "custom-pricing-model"),
		pricingConfigMap("opencost", "custom-pricing-model", map[string]string{"currencyCode": "EUR"}),
	)
	if got := resolver.resolve(active, true, now); got != "EUR" {
		t.Fatalf("resolve(active) = %q, want EUR", got)
	}

	inactiveDeployment := openCostDeployment("opencost", "opencost", "custom-pricing-model")
	inactiveDeployment.Status.AvailableReplicas = 0
	inactive := newTestCurrencyCache(t,
		inactiveDeployment,
		pricingConfigMap("opencost", "custom-pricing-model", map[string]string{"currencyCode": "EUR"}),
	)
	if got := resolver.resolve(inactive, true, now.Add(currencyDetectionTTL)); got != "EUR" {
		t.Fatalf("resolve(inactive) = %q, want last detected EUR", got)
	}

	resolver.Invalidate()
	if got := resolver.resolve(inactive, true, now.Add(2*currencyDetectionTTL)); got != pkgopencost.DefaultCurrency {
		t.Fatalf("resolve(inactive) after invalidation = %q, want %s", got, pkgopencost.DefaultCurrency)
	}
}

func TestCurrencyResolverDoesNotRetainDetectionWhenActiveConfigBecomesAmbiguous(t *testing.T) {
	now := time.Now()
	resolver := NewCurrencyResolver("")
	active := newTestCurrencyCache(t,
		openCostDeployment("one", "opencost", "custom-pricing-model"),
		pricingConfigMap("one", "custom-pricing-model", map[string]string{"currencyCode": "EUR"}),
	)
	if got := resolver.resolve(active, true, now); got != "EUR" {
		t.Fatalf("resolve(active) = %q, want EUR", got)
	}

	ambiguous := newTestCurrencyCache(t,
		openCostDeployment("one", "opencost", "custom-pricing-model"),
		pricingConfigMap("one", "custom-pricing-model", map[string]string{"currencyCode": "EUR"}),
		openCostDeployment("two", "kubecost", "pricing-configs"),
		pricingConfigMap("two", "pricing-configs", map[string]string{"currencyCode": "JPY"}),
	)
	if got := resolver.resolve(ambiguous, true, now.Add(currencyDetectionTTL)); got != pkgopencost.DefaultCurrency {
		t.Fatalf("resolve(ambiguous) = %q, want %s", got, pkgopencost.DefaultCurrency)
	}
}

func TestClusterCurrencyDetectionAllowed(t *testing.T) {
	prometheuspkg.Initialize(nil, nil, "")
	t.Cleanup(func() {
		prometheuspkg.SetManualURL("")
	})

	if !clusterCurrencyDetectionAllowed() {
		t.Fatal("cluster currency detection disabled without a manual Prometheus URL")
	}
	prometheuspkg.SetManualURL("https://prometheus.example.com")
	if clusterCurrencyDetectionAllowed() {
		t.Fatal("cluster currency detection enabled with a manual Prometheus URL")
	}
	prometheuspkg.SetManualURL("")
	if !clusterCurrencyDetectionAllowed() {
		t.Fatal("cluster currency detection did not resume after clearing the manual Prometheus URL")
	}
}
