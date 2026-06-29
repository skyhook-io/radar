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

// Every canonical chart name crdGroupToChart resolves to must classify as an
// add-on — the catalog is derived from it, so a new CRD group joins for free.
func TestAddonCatalogCoversCRDGroups(t *testing.T) {
	for group, chart := range crdGroupToChart {
		if addon, _ := ClassifyAddon(chart, "", "", "", "", ""); !addon {
			t.Errorf("crdGroupToChart[%q]=%q not classified as add-on", group, chart)
		}
	}
}
