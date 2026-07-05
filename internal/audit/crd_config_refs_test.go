package audit

import (
	"testing"

	bp "github.com/skyhook-io/radar/pkg/audit"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
			obj: map[string]any{"spec": map[string]any{"forProvider": map[string]any{
				"chart": map[string]any{"pullSecretRef": map[string]any{"namespace": "charts", "name": "oci-pull"}},
				"valuesFrom": []any{
					map[string]any{"configMapKeyRef": map[string]any{"namespace": "charts", "name": "values"}},
					map[string]any{"secretKeyRef": map[string]any{"namespace": "charts", "name": "values-secret"}},
				},
			}}},
			want: refs(secret("charts", "oci-pull"), configMap("charts", "values"), secret("charts", "values-secret")),
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
