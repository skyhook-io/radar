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
	deployments  appslisters.DeploymentLister
	statefulSets appslisters.StatefulSetLister
	configMaps   corelisters.ConfigMapLister
}

func (c testCurrencyCache) Deployments() appslisters.DeploymentLister   { return c.deployments }
func (c testCurrencyCache) StatefulSets() appslisters.StatefulSetLister { return c.statefulSets }
func (c testCurrencyCache) ConfigMaps() corelisters.ConfigMapLister     { return c.configMaps }

func newTestCurrencyCache(t *testing.T, objects ...any) testCurrencyCache {
	t.Helper()
	deploymentIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	statefulSetIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	configMapIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	for _, object := range objects {
		var err error
		switch value := object.(type) {
		case *appsv1.Deployment:
			err = deploymentIndexer.Add(value)
		case *appsv1.StatefulSet:
			err = statefulSetIndexer.Add(value)
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
		deployments:  appslisters.NewDeploymentLister(deploymentIndexer),
		statefulSets: appslisters.NewStatefulSetLister(statefulSetIndexer),
		configMaps:   corelisters.NewConfigMapLister(configMapIndexer),
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

func kubecostDeployment(namespace, configMapName string) *appsv1.Deployment {
	deployment := openCostDeployment(namespace, "kubecost-cost-analyzer", configMapName)
	deployment.Labels = map[string]string{"app.kubernetes.io/instance": "kubecost"}
	deployment.Spec.Template.Spec.Containers[0].Image = "gcr.io/kubecost1/cost-model:2.8.2"
	return deployment
}

func pricingConfigMap(namespace, name string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}, Data: data}
}

func kubecostStatefulSet(namespace, currency string) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "kubecost-aggregator"},
		Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{
			{
				Name:  "aggregator",
				Image: "icr.io/kubecost/cost-model:3.2.1",
				Env:   []corev1.EnvVar{{Name: "DISPLAY_CURRENCY", Value: currency}},
			},
		}}}},
		Status: appsv1.StatefulSetStatus{AvailableReplicas: 1},
	}
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
			name:    "kubecost 3 statefulset display currency",
			objects: []any{kubecostStatefulSet("kubecost", " aud ")},
			want:    "AUD",
		},
		{
			name: "empty kubecost display currency falls through to config map",
			objects: []any{
				kubecostStatefulSet("kubecost", " "),
				pricingConfigMap("kubecost", "pricing-configs", map[string]string{"currencyCode": "EUR"}),
			},
			want: "EUR",
		},
		{
			name: "kubecost display currency takes precedence across workloads",
			objects: []any{
				kubecostDeployment("kubecost", "pricing-configs"),
				kubecostStatefulSet("kubecost", "AUD"),
				pricingConfigMap("kubecost", "pricing-configs", map[string]string{"currencyCode": "USD"}),
			},
			want: "AUD",
		},
		{
			name: "invalid kubecost display currency is rejected",
			objects: []any{
				kubecostStatefulSet("kubecost", "dollars"),
				pricingConfigMap("kubecost", "pricing-configs", map[string]string{"currencyCode": "USD"}),
				openCostDeployment("opencost", "opencost", "custom-pricing-model"),
				pricingConfigMap("opencost", "custom-pricing-model", map[string]string{"currencyCode": "EUR"}),
			},
			want: "",
		},
		{
			name: "indirect kubecost display currency is not inferred",
			objects: []any{
				func() *appsv1.StatefulSet {
					statefulSet := kubecostStatefulSet("kubecost", "")
					statefulSet.Spec.Template.Spec.Containers[0].Env[0].ValueFrom = &corev1.EnvVarSource{
						ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "runtime-config"},
							Key:                  "currency",
						},
					}
					return statefulSet
				}(),
				pricingConfigMap("kubecost", "pricing-configs", map[string]string{"currencyCode": "USD"}),
			},
			want: "",
		},
		{
			name: "statefulset ready replicas support older kubernetes",
			objects: []any{
				func() *appsv1.StatefulSet {
					statefulSet := kubecostStatefulSet("kubecost", "AUD")
					statefulSet.Status.AvailableReplicas = 0
					statefulSet.Status.ReadyReplicas = 1
					return statefulSet
				}(),
			},
			want: "AUD",
		},
		{
			name: "conflicting kubecost display currencies are ambiguous",
			objects: []any{
				kubecostStatefulSet("kubecost-one", "AUD"),
				kubecostStatefulSet("kubecost-two", "EUR"),
			},
			want: "",
		},
		{
			name: "kubecost prometheus workload is ignored",
			objects: []any{
				&appsv1.StatefulSet{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "monitoring",
						Name:      "kubecost-prometheus-server",
						Labels:    map[string]string{"app.kubernetes.io/instance": "kubecost"},
					},
					Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{
						{Name: "prometheus-server", Image: "quay.io/prometheus/prometheus:v2.53.0"},
					}}}},
					Status: appsv1.StatefulSetStatus{ReadyReplicas: 1},
				},
				pricingConfigMap("monitoring", "pricing-configs", map[string]string{"currencyCode": "JPY"}),
				openCostDeployment("opencost", "opencost", "custom-pricing-model"),
				pricingConfigMap("opencost", "custom-pricing-model", map[string]string{"currencyCode": "EUR"}),
			},
			want: "EUR",
		},
		{
			name: "inactive kubecost statefulset is ignored",
			objects: []any{
				func() *appsv1.StatefulSet {
					statefulSet := kubecostStatefulSet("kubecost", "AUD")
					statefulSet.Status.AvailableReplicas = 0
					return statefulSet
				}(),
			},
			want: "",
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
		{
			name: "conflicting config map remains ambiguous alongside valid config",
			objects: []any{
				openCostDeployment("one", "opencost", "custom-pricing-model"),
				pricingConfigMap("one", "custom-pricing-model", map[string]string{
					"aws.json": `{"currencyCode":"USD"}`,
					"gcp.json": `{"currencyCode":"EUR"}`,
				}),
				openCostDeployment("two", "opencost", "custom-pricing-model"),
				pricingConfigMap("two", "custom-pricing-model", map[string]string{"currencyCode": "EUR"}),
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

func TestDetectOpenCostCurrencyWithIndependentWorkloadListers(t *testing.T) {
	deploymentCache := newTestCurrencyCache(t,
		openCostDeployment("opencost", "opencost", "custom-pricing-model"),
		pricingConfigMap("opencost", "custom-pricing-model", map[string]string{"currencyCode": "EUR"}),
	)
	deploymentCache.statefulSets = nil
	if got := detectOpenCostCurrency(deploymentCache); got != "EUR" {
		t.Fatalf("detectOpenCostCurrency(deployments only) = %q, want EUR", got)
	}

	statefulSetCache := newTestCurrencyCache(t, kubecostStatefulSet("kubecost", "AUD"))
	statefulSetCache.deployments = nil
	if got := detectOpenCostCurrency(statefulSetCache); got != "AUD" {
		t.Fatalf("detectOpenCostCurrency(statefulsets only) = %q, want AUD", got)
	}
}

func TestDetectOpenCostCurrencyWithoutConfigMapLister(t *testing.T) {
	kubecostCache := newTestCurrencyCache(t, kubecostStatefulSet("kubecost", "AUD"))
	kubecostCache.configMaps = nil
	if got := detectOpenCostCurrencyState(kubecostCache); got.currency != "AUD" || !got.inputsAvailable {
		t.Fatalf("detectOpenCostCurrencyState(kubecost) = %#v, want AUD with available inputs", got)
	}

	openCostCache := newTestCurrencyCache(t, openCostDeployment("opencost", "opencost", "custom-pricing-model"))
	openCostCache.configMaps = nil
	if got := detectOpenCostCurrencyState(openCostCache); got.inputsAvailable {
		t.Fatalf("detectOpenCostCurrencyState(opencost) = %#v, want unavailable inputs", got)
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

func TestCurrencyResolverRetriesWhileDetectionInputsAreUnavailable(t *testing.T) {
	now := time.Now()
	resolver := NewCurrencyResolver("")
	unavailable := testCurrencyCache{
		deployments: newTestCurrencyCache(t).deployments,
	}
	if got := resolver.resolve(unavailable, true, now); got != pkgopencost.DefaultCurrency {
		t.Fatalf("resolve(unavailable) = %q, want %s", got, pkgopencost.DefaultCurrency)
	}
	if !resolver.expiresAt.IsZero() {
		t.Fatalf("resolve(unavailable) cached fallback until %s", resolver.expiresAt)
	}

	ready := newTestCurrencyCache(t,
		openCostDeployment("opencost", "opencost", "custom-pricing-model"),
		pricingConfigMap("opencost", "custom-pricing-model", map[string]string{"currencyCode": "EUR"}),
	)
	if got := resolver.resolve(ready, true, now.Add(time.Second)); got != "EUR" {
		t.Fatalf("resolve(ready) = %q, want EUR", got)
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

func TestCurrencyResolverRetainsAndResetsStatefulSetDetection(t *testing.T) {
	now := time.Now()
	resolver := NewCurrencyResolver("")
	active := newTestCurrencyCache(t, kubecostStatefulSet("kubecost", "AUD"))
	if got := resolver.resolve(active, true, now); got != "AUD" {
		t.Fatalf("resolve(active statefulset) = %q, want AUD", got)
	}

	inactiveStatefulSet := kubecostStatefulSet("kubecost", "AUD")
	inactiveStatefulSet.Status.AvailableReplicas = 0
	inactive := newTestCurrencyCache(t, inactiveStatefulSet)
	if got := resolver.resolve(inactive, true, now.Add(currencyDetectionTTL)); got != "AUD" {
		t.Fatalf("resolve(inactive statefulset) = %q, want last detected AUD", got)
	}

	invalid := newTestCurrencyCache(t, kubecostStatefulSet("kubecost", "dollars"))
	if got := resolver.resolve(invalid, true, now.Add(2*currencyDetectionTTL)); got != pkgopencost.DefaultCurrency {
		t.Fatalf("resolve(invalid active statefulset) = %q, want %s", got, pkgopencost.DefaultCurrency)
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
	originalCostConfig := ConfigSnapshot()
	t.Cleanup(func() {
		prometheuspkg.SetManualURL("")
		_ = Configure(originalCostConfig)
	})

	if !clusterCurrencyDetectionAllowed() {
		t.Fatal("cluster currency detection disabled without a manual Prometheus URL")
	}
	prometheuspkg.SetManualURL("https://prometheus.example.com")
	if clusterCurrencyDetectionAllowed() {
		t.Fatal("cluster currency detection enabled with a manual Prometheus URL")
	}
	if err := Configure(ManagerConfig{Source: SourceKubecost}); err != nil {
		t.Fatal(err)
	}
	if !clusterCurrencyDetectionAllowed() {
		t.Fatal("Kubecost source should use currency evidence from the connected cluster")
	}
	if err := Configure(ManagerConfig{Source: SourceAuto}); err != nil {
		t.Fatal(err)
	}
	prometheuspkg.SetManualURL("")
	if !clusterCurrencyDetectionAllowed() {
		t.Fatal("cluster currency detection did not resume after clearing the manual Prometheus URL")
	}
}
