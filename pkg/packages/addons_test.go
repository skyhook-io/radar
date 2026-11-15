package packages

import "testing"

func TestClassifyAddon(t *testing.T) {
	cases := []struct {
		name                             string
		chart, app, partOf, wl, addonMgr string
		image                            string
		wantAddon                        bool
		wantWhy                          string
	}{
		{
			name: "catalog chart, exact", chart: "cert-manager-v1.14.0",
			wantAddon: true, wantWhy: "chart=cert-manager",
		},
		{
			name: "catalog chart with numeric version suffix", chart: "kube-prometheus-stack-45.27.2",
			wantAddon: true, wantWhy: "chart=kube-prometheus-stack",
		},
		{
			name: "sub-name hits base via prefix", app: "cert-manager-webhook",
			wantAddon: true, wantWhy: "app.kubernetes.io/name=cert-manager-webhook (cert-manager)",
		},
		{
			name: "part-of label", partOf: "flux",
			wantAddon: true, wantWhy: "app.kubernetes.io/part-of=flux",
		},
		{
			name: "extras-only chart (no CRD group)", wl: "ingress-nginx-controller",
			wantAddon: true, wantWhy: "name=ingress-nginx-controller (ingress-nginx)",
		},
		{
			name: "crossplane (extras)", chart: "crossplane",
			wantAddon: true, wantWhy: "chart=crossplane",
		},
		{
			name: "user service, no match", chart: "payments-1.2.0", app: "payments", wl: "payments-api",
			wantAddon: false, wantWhy: "",
		},
		{
			name: "empty", wantAddon: false, wantWhy: "",
		},
		{
			name: "user service named like Gateway API", wl: "gateway-api-v2",
			wantAddon: false, wantWhy: "",
		},
		{
			name: "user LeaderWorkerSet named like the operator", wl: "lws-storage-sample",
			wantAddon: false, wantWhy: "",
		},
		{
			name: "LeaderWorkerSet controller installed by its chart", chart: "lws-0.7.0", wl: "lws-controller-manager",
			wantAddon: true, wantWhy: "chart=lws",
		},
		{
			name: "user model server deployed with the llm-d chart", chart: "llm-d-modelservice-0.2.1", wl: "ms-llama-decode",
			wantAddon: false, wantWhy: "",
		},
		{
			name: "user model deployment named like a model platform", wl: "kserve-fraud", app: "kserve-fraud",
			wantAddon: false, wantWhy: "",
		},
		{
			name: "integration without name matching needs its chart", wl: "gpu-operator",
			wantAddon: false, wantWhy: "",
		},
		{
			name: "integration classified by its chart", chart: "gpu-operator-v24.9.0", wl: "gpu-operator",
			wantAddon: true, wantWhy: "chart=gpu-operator",
		},
		{
			name: "user chart named like a model platform", chart: "kserve-fraud-1.0.0", wl: "fraud-predictor",
			wantAddon: false, wantWhy: "",
		},
		{
			name: "user chart named like a workload API", chart: "lws-storage-sample-0.1.0",
			wantAddon: false, wantWhy: "",
		},
		{
			name: "integration that accepts name matching", wl: "argo-cd-server",
			wantAddon: true, wantWhy: "name=argo-cd-server (argo-cd)",
		},
		{
			// The upstream addon-manager label is the platform declaring
			// "managed addon" — wins regardless of catalog coverage.
			name: "addonmanager label (GKE-managed component)", wl: "networking-dra-driver", addonMgr: "Reconcile",
			wantAddon: true, wantWhy: "addonmanager.kubernetes.io/mode=Reconcile",
		},
		{
			name: "victoria-metrics family via prefix", chart: "victoria-metrics-single-0.8.48",
			wantAddon: true, wantWhy: "chart=victoria-metrics-single (victoria-metrics)",
		},
		{
			name: "caretta", chart: "caretta-0.0.16",
			wantAddon: true, wantWhy: "chart=caretta",
		},
		{
			name: "chart wins over later candidates", chart: "argo-cd", app: "payments",
			wantAddon: true, wantWhy: "chart=argo-cd",
		},
		{
			name: "generic name alone is not enough", app: "vector", wl: "vector",
			wantAddon: false, wantWhy: "",
		},
		{
			name: "generic name with known image repo", app: "vector", wl: "vector", image: "docker.io/timberio/vector:0.48.0",
			wantAddon: true, wantWhy: "image=docker.io/timberio/vector (vector)",
		},
		{
			name: "generic chart is strong evidence", chart: "vault-0.29.1", app: "vault",
			wantAddon: true, wantWhy: "chart=vault",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotAddon, gotWhy := ClassifyAddon(c.chart, c.app, c.partOf, c.wl, c.addonMgr, c.image)
			if gotAddon != c.wantAddon || gotWhy != c.wantWhy {
				t.Errorf("ClassifyAddon(%q,%q,%q,%q) = (%v,%q), want (%v,%q)",
					c.chart, c.app, c.partOf, c.wl, gotAddon, gotWhy, c.wantAddon, c.wantWhy)
			}
		})
	}
}

// Every chart name crdGroupToChart resolves to must classify as an add-on —
// the catalog is derived from it, so a new CRD group joins for free — except
// the names integrationsNotAddons holds back.
func TestAddonCatalogCoversCRDGroups(t *testing.T) {
	for group, chart := range crdGroupToChart {
		addon, _ := ClassifyAddon(chart, "", "", "", "", "")
		if want := !integrationsNotAddons[chart]; addon != want {
			t.Errorf("crdGroupToChart[%q]=%q classified as add-on=%v, want %v", group, chart, addon, want)
		}
	}
	mapped := map[string]bool{}
	for _, chart := range crdGroupToChart {
		mapped[chart] = true
	}
	for name := range integrationsNotAddons {
		if !mapped[name] {
			t.Errorf("integrationsNotAddons lists %q, which no crdGroupToChart entry maps to", name)
		}
	}
	for _, name := range nameMatchedIntegrations {
		if !mapped[name] {
			t.Errorf("nameMatchedIntegrations lists %q, which no crdGroupToChart entry maps to", name)
		}
	}
}
