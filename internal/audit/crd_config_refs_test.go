package audit

import (
	"context"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	bp "github.com/skyhook-io/radar/pkg/audit"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestDynamicConfigObjectRefs(t *testing.T) {
	tests := []struct {
		name string
		gvr  schema.GroupVersionResource
		obj  map[string]any
		ns   string
		want []bp.ConfigObjectRef
	}{
		{
			name: "gateway certificate refs",
			gvr:  gvr("gateway.networking.k8s.io", "v1", "gateways"),
			ns:   "edge",
			obj: map[string]any{"spec": map[string]any{"listeners": []any{
				map[string]any{"tls": map[string]any{"certificateRefs": []any{
					map[string]any{"name": "edge-cert"},
					map[string]any{"name": "shared-cert", "namespace": "infra", "kind": "Secret"},
					map[string]any{"name": "ignored-config", "kind": "ConfigMap"},
				}}},
			}}},
			want: refs(secret("edge", "edge-cert"), secret("infra", "shared-cert")),
		},
		{
			name: "traefik route and tls resources",
			gvr:  gvr("traefik.io", "v1alpha1", "serverstransports"),
			ns:   "edge",
			obj: map[string]any{"spec": map[string]any{
				"rootCAsSecrets":      []any{"root-ca"},
				"certificatesSecrets": []any{map[string]any{"name": "client-cert"}},
			}},
			want: refs(secret("edge", "root-ca"), secret("edge", "client-cert")),
		},
		{
			name: "traefik middleware auth refs",
			gvr:  gvr("traefik.io", "v1alpha1", "middlewares"),
			ns:   "edge",
			obj: map[string]any{"spec": map[string]any{
				"basicAuth":  map[string]any{"secret": "basic-users"},
				"digestAuth": map[string]any{"secret": "digest-users"},
				"forwardAuth": map[string]any{"tls": map[string]any{
					"caSecret": "forward-ca",
				}},
			}},
			want: refs(secret("edge", "basic-users"), secret("edge", "digest-users"), secret("edge", "forward-ca")),
		},
		{
			name: "contour httpproxy tls",
			gvr:  gvr("projectcontour.io", "v1", "httpproxies"),
			ns:   "edge",
			obj:  map[string]any{"spec": map[string]any{"virtualhost": map[string]any{"tls": map[string]any{"secretName": "contour-cert"}}}},
			want: refs(secret("edge", "contour-cert")),
		},
		{
			name: "istio gateway tls credentials",
			gvr:  gvr("networking.istio.io", "v1", "gateways"),
			ns:   "istio-system",
			obj: map[string]any{"spec": map[string]any{"servers": []any{
				map[string]any{"tls": map[string]any{"credentialName": "gateway-cert"}},
				map[string]any{"tls": map[string]any{"credentialNames": []any{"api-cert", map[string]any{"name": "admin-cert"}}}},
				map[string]any{"port": map[string]any{"number": int64(80)}},
			}}},
			want: refs(secret("istio-system", "gateway-cert"), secret("istio-system", "api-cert"), secret("istio-system", "admin-cert")),
		},
		{
			name: "cert-manager issuer acme refs",
			gvr:  gvr("cert-manager.io", "v1", "issuers"),
			ns:   "certs",
			obj: map[string]any{"spec": map[string]any{"acme": map[string]any{
				"privateKeySecretRef": map[string]any{"name": "issuer-account-key"},
				"solvers": []any{map[string]any{"dns01": map[string]any{"cloudDNS": map[string]any{
					"serviceAccountSecretRef": map[string]any{"name": "cloud-dns-key", "key": "key.json"},
				}}}},
			}}},
			want: refs(secret("certs", "issuer-account-key"), secret("certs", "cloud-dns-key")),
		},
		{
			name: "cert-manager clusterissuer acme refs",
			gvr:  gvr("cert-manager.io", "v1", "clusterissuers"),
			obj: map[string]any{"spec": map[string]any{"acme": map[string]any{
				"privateKeySecretRef": map[string]any{"name": "cluster-account-key"},
				"solvers": []any{map[string]any{"dns01": map[string]any{"route53": map[string]any{
					"secretAccessKeySecretRef": map[string]any{"name": "route53-secret", "key": "secret-access-key"},
				}}}},
			}}},
			want: refs(secret("cert-manager", "cluster-account-key"), secret("cert-manager", "route53-secret")),
		},
		{
			name: "flux kustomization refs",
			gvr:  gvr("kustomize.toolkit.fluxcd.io", "v1", "kustomizations"),
			ns:   "gitops",
			obj: map[string]any{"spec": map[string]any{
				"decryption": map[string]any{"secretRef": map[string]any{"name": "sops-age"}},
				"kubeConfig": map[string]any{
					"secretRef":    map[string]any{"name": "remote-kubeconfig"},
					"configMapRef": map[string]any{"name": "remote-kubeconfig-ca"},
				},
				"postBuild": map[string]any{"substituteFrom": []any{
					map[string]any{"kind": "ConfigMap", "name": "substitutions"},
					map[string]any{"kind": "Secret", "name": "secret-substitutions"},
				}},
			}},
			want: refs(
				secret("gitops", "sops-age"),
				secret("gitops", "remote-kubeconfig"),
				configMap("gitops", "remote-kubeconfig-ca"),
				configMap("gitops", "substitutions"),
				secret("gitops", "secret-substitutions"),
			),
		},
		{
			name: "flux helmrelease refs",
			gvr:  gvr("helm.toolkit.fluxcd.io", "v2", "helmreleases"),
			ns:   "gitops",
			obj: map[string]any{"spec": map[string]any{
				"kubeConfig": map[string]any{"configMapRef": map[string]any{"name": "cluster-ca"}},
				"chart":      map[string]any{"spec": map[string]any{"verify": map[string]any{"secretRef": map[string]any{"name": "cosign-key"}}}},
				"valuesFrom": []any{
					map[string]any{"name": "chart-values"},
					map[string]any{"kind": "Secret", "name": "chart-secrets"},
				},
			}},
			want: refs(configMap("gitops", "cluster-ca"), secret("gitops", "cosign-key"), configMap("gitops", "chart-values"), secret("gitops", "chart-secrets")),
		},
		{
			name: "flux source refs",
			gvr:  gvr("source.toolkit.fluxcd.io", "v1", "gitrepositories"),
			ns:   "gitops",
			obj: map[string]any{"spec": map[string]any{
				"secretRef":      map[string]any{"name": "git-credentials"},
				"proxySecretRef": map[string]any{"name": "proxy-credentials"},
				"verify":         map[string]any{"secretRef": map[string]any{"name": "gpg-keyring"}},
			}},
			want: refs(secret("gitops", "git-credentials"), secret("gitops", "proxy-credentials"), secret("gitops", "gpg-keyring")),
		},
		{
			name: "external secret target and template refs",
			gvr:  gvr("external-secrets.io", "v1", "externalsecrets"),
			ns:   "app",
			obj: map[string]any{
				"metadata": map[string]any{"name": "db-creds"},
				"spec": map[string]any{"target": map[string]any{
					"name": "db-creds",
					"template": map[string]any{"templateFrom": []any{
						map[string]any{"configMap": map[string]any{"name": "secret-template"}},
						map[string]any{"secret": map[string]any{"name": "template-secret"}},
					}},
				}},
			},
			want: refs(secret("app", "db-creds"), configMap("app", "secret-template"), secret("app", "template-secret")),
		},
		{
			name: "keda trigger auth refs",
			gvr:  gvr("keda.sh", "v1alpha1", "triggerauthentications"),
			ns:   "app",
			obj: map[string]any{"spec": map[string]any{
				"secretTargetRef":    []any{map[string]any{"name": "queue-secret"}},
				"configMapTargetRef": []any{map[string]any{"name": "queue-config"}},
				"gcpSecretManager":   map[string]any{"credentials": map[string]any{"clientSecret": map[string]any{"name": "gcp-secret"}}},
			}},
			want: refs(secret("app", "queue-secret"), configMap("app", "queue-config"), secret("app", "gcp-secret")),
		},
		{
			name: "prometheus servicemonitor refs",
			gvr:  gvr("monitoring.coreos.com", "v1", "servicemonitors"),
			ns:   "monitoring",
			obj: map[string]any{"spec": map[string]any{"endpoints": []any{
				map[string]any{
					"authorization": map[string]any{"credentials": map[string]any{"name": "bearer-secret"}},
					"basicAuth": map[string]any{
						"username": map[string]any{"name": "basic-user"},
						"password": map[string]any{"name": "basic-pass"},
					},
					"tlsConfig": map[string]any{
						"ca":        map[string]any{"configMap": map[string]any{"name": "ca-config"}},
						"keySecret": map[string]any{"name": "client-key"},
					},
				},
			}}},
			want: refs(secret("monitoring", "bearer-secret"), secret("monitoring", "basic-user"), secret("monitoring", "basic-pass"), configMap("monitoring", "ca-config"), secret("monitoring", "client-key")),
		},
		{
			name: "alertmanager top-level refs",
			gvr:  gvr("monitoring.coreos.com", "v1", "alertmanagers"),
			ns:   "monitoring",
			obj: map[string]any{"spec": map[string]any{
				"secrets":          []any{"am-secret"},
				"configMaps":       []any{"am-config"},
				"configSecret":     "am-main-config",
				"imagePullSecrets": []any{map[string]any{"name": "am-pull"}},
			}},
			want: refs(secret("monitoring", "am-secret"), configMap("monitoring", "am-config"), secret("monitoring", "am-main-config"), secret("monitoring", "am-pull")),
		},
		{
			name: "crossplane provider config explicit namespace refs",
			gvr:  gvr("kubernetes.crossplane.io", "v1alpha2", "providerconfigs"),
			obj: map[string]any{"spec": map[string]any{"credentials": map[string]any{"secretRef": map[string]any{
				"namespace": "crossplane-system",
				"name":      "provider-creds",
			}}}},
			want: refs(secret("crossplane-system", "provider-creds")),
		},
		{
			name: "crossplane helm release explicit refs",
			gvr:  gvr("helm.crossplane.io", "v1beta1", "releases"),
			ns:   "crossplane-system",
			obj: map[string]any{"spec": map[string]any{"forProvider": map[string]any{
				"chart": map[string]any{"pullSecretRef": map[string]any{"name": "oci-pull"}},
				"valuesFrom": []any{
					map[string]any{"configMapKeyRef": map[string]any{"namespace": "charts", "name": "values"}},
					map[string]any{"secretKeyRef": map[string]any{"name": "values-secret"}},
				},
				"patchesFrom": []any{
					map[string]any{"valueFrom": map[string]any{"configMapKeyRef": map[string]any{"name": "patches"}}},
				},
			}}},
			want: refs(secret("crossplane-system", "oci-pull"), configMap("charts", "values"), secret("crossplane-system", "values-secret"), configMap("crossplane-system", "patches")),
		},
		{
			name: "rollout pod template refs",
			gvr:  gvr("argoproj.io", "v1alpha1", "rollouts"),
			ns:   "app",
			obj: map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
				"imagePullSecrets": []any{map[string]any{"name": "pull-secret"}},
				"containers": []any{map[string]any{
					"envFrom": []any{map[string]any{"configMapRef": map[string]any{"name": "rollout-config"}}},
					"env":     []any{map[string]any{"valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "rollout-secret"}}}},
				}},
				"volumes": []any{map[string]any{"projected": map[string]any{"sources": []any{
					map[string]any{"configMap": map[string]any{"name": "projected-config"}},
				}}}},
			}}}},
			want: refs(secret("app", "pull-secret"), configMap("app", "rollout-config"), secret("app", "rollout-secret"), configMap("app", "projected-config")),
		},
		{
			name: "cnpg cluster refs",
			gvr:  gvr("postgresql.cnpg.io", "v1", "clusters"),
			ns:   "db",
			obj: map[string]any{"spec": map[string]any{
				"monitoring": map[string]any{"customQueriesConfigMap": []any{map[string]any{"name": "pg-queries"}}},
				"bootstrap": map[string]any{"initdb": map[string]any{
					"secret":                     map[string]any{"name": "bootstrap-secret"},
					"postInitSQLRefs":            map[string]any{"configMapRefs": []any{map[string]any{"name": "post-init-sql"}}},
					"postInitApplicationSQLRefs": map[string]any{"secretRefs": []any{map[string]any{"name": "post-init-secret"}}},
				}},
				"certificates":    map[string]any{"serverTLSSecret": "server-tls"},
				"superuserSecret": map[string]any{"name": "postgres-superuser"},
			}},
			want: refs(configMap("db", "pg-queries"), secret("db", "bootstrap-secret"), configMap("db", "post-init-sql"), secret("db", "post-init-secret"), secret("db", "server-tls"), secret("db", "postgres-superuser")),
		},
		{
			name: "capi kubeadm file refs",
			gvr:  gvr("bootstrap.cluster.x-k8s.io", "v1beta1", "kubeadmconfigs"),
			ns:   "capi",
			obj: map[string]any{"spec": map[string]any{"files": []any{
				map[string]any{"contentFrom": map[string]any{"secret": map[string]any{"name": "cloud-init-fragment"}}},
			}}},
			want: refs(secret("capi", "cloud-init-fragment")),
		},
		{
			name: "velero location credential",
			gvr:  gvr("velero.io", "v1", "backupstoragelocations"),
			ns:   "velero",
			obj: map[string]any{"spec": map[string]any{
				"credential":    map[string]any{"name": "cloud-creds"},
				"objectStorage": map[string]any{"caCertRef": map[string]any{"name": "object-store-ca"}},
			}},
			want: refs(secret("velero", "cloud-creds"), secret("velero", "object-store-ca")),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := dynamicConfigRefHandlerFor(tt.gvr)
			if handler == nil {
				t.Fatalf("no handler for %v", tt.gvr)
			}
			u := &unstructured.Unstructured{Object: tt.obj}
			u.SetNamespace(tt.ns)
			u.SetName("subject")
			got := handler(u)
			assertRefSet(t, got, tt.want)
		})
	}
}

func TestDynamicConfigObjectRefsUnhandledKinds(t *testing.T) {
	if handler := dynamicConfigRefHandlerFor(gvr("karpenter.sh", "v1", "nodepools")); handler != nil {
		t.Fatalf("Karpenter NodePool should not have config ref handler")
	}
}

func TestListDynamicConfigObjectRefsFiltersByTargetNamespace(t *testing.T) {
	gatewayGVR := gvr("gateway.networking.k8s.io", "v1", "gateways")
	gateway := testUnstructured("gateway.networking.k8s.io/v1", "Gateway", "edge", "public-gw", map[string]any{
		"spec": map[string]any{"listeners": []any{
			map[string]any{"tls": map[string]any{"certificateRefs": []any{
				map[string]any{"name": "edge-cert"},
				map[string]any{"name": "shared-cert", "namespace": "infra", "kind": "Secret"},
				map[string]any{"name": "shared-cert", "namespace": "infra", "kind": "Secret"},
			}}},
		}},
	})
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gatewayGVR: "GatewayList"},
	)
	if _, err := dynClient.Resource(gatewayGVR).Namespace("edge").Create(context.Background(), gateway, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create Gateway fixture: %v", err)
	}
	if err := k8s.InitTestDynamicResourceCache(dynClient, []k8s.APIResource{
		{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway", Name: "gateways", Namespaced: true, Verbs: []string{"list", "watch"}},
	}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	defer k8s.ResetTestDynamicState()

	dynCache := k8s.GetDynamicResourceCache()
	if err := dynCache.EnsureWatching(gatewayGVR); err != nil {
		t.Fatalf("EnsureWatching: %v", err)
	}
	if !dynCache.WaitForSync(gatewayGVR, 2*time.Second) {
		t.Fatal("dynamic Gateway cache did not sync")
	}
	items, err := dynCache.ListWatched(gatewayGVR)
	if err != nil {
		t.Fatalf("ListWatched: %v", err)
	}
	if len(items) == 0 {
		t.Fatalf("expected Gateway in dynamic cache, got none")
	}

	got := listDynamicConfigObjectRefs([]string{"infra"}, dynamicConfigRefOptions{})
	assertRefSet(t, got, refs(secret("infra", "shared-cert")))
}

func TestListDynamicConfigObjectRefsAddsServiceAccountImagePullSecretsForPodTemplates(t *testing.T) {
	rolloutGVR := gvr("argoproj.io", "v1alpha1", "rollouts")
	rollout := testUnstructured("argoproj.io/v1alpha1", "Rollout", "app", "web", map[string]any{
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"serviceAccountName": "builder",
			"containers": []any{map[string]any{
				"name":  "web",
				"image": "example.com/web:latest",
			}},
		}}},
	})
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{rolloutGVR: "RolloutList"},
	)
	if _, err := dynClient.Resource(rolloutGVR).Namespace("app").Create(context.Background(), rollout, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create Rollout fixture: %v", err)
	}
	if err := k8s.InitTestDynamicResourceCache(dynClient, []k8s.APIResource{
		{Group: "argoproj.io", Version: "v1alpha1", Kind: "Rollout", Name: "rollouts", Namespaced: true, Verbs: []string{"list", "watch"}},
	}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	defer k8s.ResetTestDynamicState()

	dynCache := k8s.GetDynamicResourceCache()
	if err := dynCache.EnsureWatching(rolloutGVR); err != nil {
		t.Fatalf("EnsureWatching: %v", err)
	}
	if !dynCache.WaitForSync(rolloutGVR, 2*time.Second) {
		t.Fatal("dynamic Rollout cache did not sync")
	}

	got := listDynamicConfigObjectRefs([]string{"app"}, dynamicConfigRefOptions{
		ServiceAccounts: []*corev1.ServiceAccount{{
			ObjectMeta: metav1.ObjectMeta{Name: "builder", Namespace: "app"},
			ImagePullSecrets: []corev1.LocalObjectReference{
				{Name: "registry-creds"},
			},
		}},
	})
	assertRefSet(t, got, refs(secret("app", "registry-creds")))
}

func TestListDynamicConfigObjectRefsUsesCertManagerClusterResourceNamespace(t *testing.T) {
	clusterIssuerGVR := gvr("cert-manager.io", "v1", "clusterissuers")
	clusterIssuer := testUnstructured("cert-manager.io/v1", "ClusterIssuer", "", "letsencrypt", map[string]any{
		"spec": map[string]any{"acme": map[string]any{
			"privateKeySecretRef": map[string]any{"name": "issuer-account-key"},
			"solvers": []any{map[string]any{"dns01": map[string]any{"cloudDNS": map[string]any{
				"serviceAccountSecretRef": map[string]any{"name": "cloud-dns-key"},
			}}}},
		}},
	})
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{clusterIssuerGVR: "ClusterIssuerList"},
	)
	if _, err := dynClient.Resource(clusterIssuerGVR).Create(context.Background(), clusterIssuer, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create ClusterIssuer fixture: %v", err)
	}
	if err := k8s.InitTestDynamicResourceCache(dynClient, []k8s.APIResource{
		{Group: "cert-manager.io", Version: "v1", Kind: "ClusterIssuer", Name: "clusterissuers", Namespaced: false, Verbs: []string{"list", "watch"}},
	}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	defer k8s.ResetTestDynamicState()

	dynCache := k8s.GetDynamicResourceCache()
	if err := dynCache.EnsureWatching(clusterIssuerGVR); err != nil {
		t.Fatalf("EnsureWatching: %v", err)
	}
	if !dynCache.WaitForSync(clusterIssuerGVR, 2*time.Second) {
		t.Fatal("dynamic ClusterIssuer cache did not sync")
	}

	got := listDynamicConfigObjectRefs([]string{"certs-system"}, dynamicConfigRefOptions{
		Deployments: []*appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "cert-manager",
				Namespace: "cert-manager",
				Labels:    map[string]string{"app.kubernetes.io/name": "cert-manager"},
			},
			Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name: "cert-manager",
					Args: []string{"--cluster-resource-namespace=certs-system"},
				}},
			}}},
		}},
	})
	assertRefSet(t, got, refs(secret("certs-system", "issuer-account-key"), secret("certs-system", "cloud-dns-key")))
}

func assertRefSet(t *testing.T, got, want []bp.ConfigObjectRef) {
	t.Helper()
	gotSet := refSet(got)
	wantSet := refSet(want)
	if len(gotSet) != len(wantSet) {
		t.Fatalf("got refs %+v, want %+v", got, want)
	}
	for key := range wantSet {
		if !gotSet[key] {
			t.Fatalf("missing ref %s in %+v", key, got)
		}
	}
	for key := range gotSet {
		if !wantSet[key] {
			t.Fatalf("unexpected ref %s in %+v", key, got)
		}
	}
}

func refSet(refs []bp.ConfigObjectRef) map[string]bool {
	out := map[string]bool{}
	for _, ref := range refs {
		out[ref.Kind+"/"+ref.Namespace+"/"+ref.Name] = true
	}
	return out
}

func gvr(group, version, resource string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: group, Version: version, Resource: resource}
}

func refs(refs ...bp.ConfigObjectRef) []bp.ConfigObjectRef {
	return refs
}

func configMap(ns, name string) bp.ConfigObjectRef {
	return bp.ConfigObjectRef{Kind: "ConfigMap", Namespace: ns, Name: name}
}

func secret(ns, name string) bp.ConfigObjectRef {
	return bp.ConfigObjectRef{Kind: "Secret", Namespace: ns, Name: name}
}

func testUnstructured(apiVersion, kind, ns, name string, obj map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: obj}
	u.SetAPIVersion(apiVersion)
	u.SetKind(kind)
	u.SetNamespace(ns)
	u.SetName(name)
	u.SetCreationTimestamp(metav1.Now())
	return u
}
