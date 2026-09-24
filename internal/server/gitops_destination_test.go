package server

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const testKubeconfig = `apiVersion: v1
kind: Config
current-context: prod
clusters:
- name: prod
  cluster:
    server: https://prod.example.com:6443/some/path?token=x
- name: other
  cluster:
    server: https://other.example.com
contexts:
- name: prod
  context: {cluster: prod, user: u}
users:
- name: u
  user: {token: secret}
`

func fluxObj(kubeConfig map[string]any) *unstructured.Unstructured {
	spec := map[string]any{}
	if kubeConfig != nil {
		spec["kubeConfig"] = kubeConfig
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": "flux-system", "name": "fleet-prod"},
		"spec":     spec,
	}}
}

func TestFluxTargetServer(t *testing.T) {
	secrets := map[string]*corev1.Secret{
		"flux-system/default-key": {Data: map[string][]byte{"value": []byte(testKubeconfig)}},
		"flux-system/yaml-key":    {Data: map[string][]byte{"value.yaml": []byte(testKubeconfig)}},
		"flux-system/custom-key":  {Data: map[string][]byte{"cfg": []byte(testKubeconfig)}},
		"flux-system/garbage":     {Data: map[string][]byte{"value": []byte("not a kubeconfig")}},
	}
	getSecret := func(_ context.Context, ns, name string) (*corev1.Secret, error) {
		if name == "forbidden" {
			return nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, name, nil)
		}
		if s, ok := secrets[ns+"/"+name]; ok {
			return s, nil
		}
		return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, name)
	}
	getCM := func(_ context.Context, ns, name string) (*corev1.ConfigMap, error) {
		if name == "wi" {
			return &corev1.ConfigMap{Data: map[string]string{"address": "https://wi.example.com/"}}, nil
		}
		return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, name)
	}
	cases := []struct {
		name       string
		kubeConfig map[string]any
		want       string
		wantErr    bool
	}{
		{"no kubeConfig targets this cluster", nil, "", false},
		{"default key", map[string]any{"secretRef": map[string]any{"name": "default-key"}}, "https://prod.example.com:6443/some/path?token=x", false},
		{"value.yaml fallback", map[string]any{"secretRef": map[string]any{"name": "yaml-key"}}, "https://prod.example.com:6443/some/path?token=x", false},
		{"explicit key", map[string]any{"secretRef": map[string]any{"name": "custom-key", "key": "cfg"}}, "https://prod.example.com:6443/some/path?token=x", false},
		{"unparseable kubeconfig", map[string]any{"secretRef": map[string]any{"name": "garbage"}}, "", false},
		{"missing secret", map[string]any{"secretRef": map[string]any{"name": "absent"}}, "", false},
		{"forbidden secret is an error", map[string]any{"secretRef": map[string]any{"name": "forbidden"}}, "", true},
		{"workload identity address", map[string]any{"configMapRef": map[string]any{"name": "wi"}}, "https://wi.example.com/", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fluxTargetServer(context.Background(), fluxObj(tc.kubeConfig), getSecret, getCM)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("server = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestArgoDestinationServer(t *testing.T) {
	registered := map[string][]string{
		"prod-spoke": {"https://rancher.example.com/k8s/clusters/c-prod"},
		"dup":        {"https://a.example.com", "https://b.example.com"},
	}
	lookup := func(_, name string) []string { return registered[name] }
	app := func(dest map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"destination": dest}}}
	}
	cases := []struct {
		name string
		dest map[string]any
		want string
	}{
		{"server kept whole, path included", map[string]any{"server": "https://rancher.example.com/k8s/clusters/c-prod"}, "https://rancher.example.com/k8s/clusters/c-prod"},
		{"registered name", map[string]any{"name": "prod-spoke"}, "https://rancher.example.com/k8s/clusters/c-prod"},
		{"ambiguous name resolves to nothing", map[string]any{"name": "dup"}, ""},
		{"unknown name", map[string]any{"name": "nope"}, ""},
		{"in-cluster is not a destination elsewhere", map[string]any{"server": "https://kubernetes.default.svc"}, ""},
		{"no destination", map[string]any{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := argoDestinationServer(app(tc.dest), lookup); got != tc.want {
				t.Errorf("server = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRegisteredClusterServers(t *testing.T) {
	sec := func(ns, name, server string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns},
			Data:       map[string][]byte{"name": []byte(name), "server": []byte(server)},
		}
	}
	single := []*corev1.Secret{sec("argocd", "prod", "https://prod.example.com")}
	two := []*corev1.Secret{
		sec("argocd", "prod", "https://prod.example.com"),
		sec("team-argo", "prod", "https://team-prod.example.com"),
	}
	cases := []struct {
		name     string
		secrets  []*corev1.Secret
		scopedTo string
		appNS    string
		want     []string
	}{
		{"one install serves apps in any namespace", single, "", "apps", []string{"https://prod.example.com"}},
		{"the app's own install wins", two, "", "team-argo", []string{"https://team-prod.example.com"}},
		{"several installs and none is the app's: unknown", two, "", "apps", nil},
		{"cache scoped to the app's namespace", single, "argocd", "argocd", []string{"https://prod.example.com"}},
		{"cache scoped elsewhere can't see the owner", single, "argocd", "apps", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := registeredClusterServers(tc.secrets, tc.scopedTo, tc.appNS, "prod")
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("servers = %v, want %v", got, tc.want)
			}
		})
	}
}
