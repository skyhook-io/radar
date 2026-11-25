package vulnerabilities

import (
	"context"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// benchInputs is images × perImage findings in which no two images share a
// package state, the case the per-request upgrade memo can't help; reports
// carry a UID and resourceVersion as informer objects do.
func benchInputs(images, perImage int) ([]*unstructured.Unstructured, []target) {
	var items []*unstructured.Unstructured
	var targets []target
	for i := 0; i < images; i++ {
		vs := make([]vuln, perImage)
		for k := range vs {
			vs[k] = vuln{fmt.Sprintf("CVE-2026-%05d", k), []string{"CRITICAL", "HIGH", "MEDIUM", "LOW"}[k%4], fmt.Sprintf("pkg%d", k%60), fmt.Sprintf("1.%d.3", i), fmt.Sprintf("1.%d.9, %d.0.1", i, i+2)}
		}
		owner := fmt.Sprintf("app%d-1", i)
		u := vulnReport(reportSpec{name: owner, ns: "shop", ownerKind: "ReplicaSet", ownerName: owner, container: "app", repo: fmt.Sprintf("acme/app%d", i), tag: "1", digest: fmt.Sprintf("sha256:%d", i), scanned: "2026-11-10T00:00:00Z", vulns: vs})
		for _, v := range u.Object["report"].(map[string]any)["vulnerabilities"].([]any) {
			v.(map[string]any)["packagePURL"] = "pkg:npm/x"
		}
		u.SetUID(types.UID(fmt.Sprintf("uid-%d-%d", images, i)))
		u.SetResourceVersion("1")
		items = append(items, u)
		targets = append(targets, liveTarget("shop", "ReplicaSet", owner, "app", fmt.Sprintf("acme/app%d:1", i), "", WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: fmt.Sprintf("app%d", i)}))
	}
	return items, targets
}

// BenchmarkBuild is one rollup at the findings budget (400 × 500) and at a
// smaller scope, after the first build has filled the parse cache.
func BenchmarkBuild(b *testing.B) {
	for _, size := range []struct{ images, per int }{{100, 200}, {400, 500}} {
		items, targets := benchInputs(size.images, size.per)
		for _, view := range []View{ViewImage, ViewFix} {
			b.Run(fmt.Sprintf("%dx%d/%s", size.images, size.per, view), func(b *testing.B) {
				b.ReportAllocs()
				for range b.N {
					if _, err := build(context.Background(), items, targets, view, Filter{}, false); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkExport is one CSV export of each view at the findings budget:
// the uncapped rollup plus writing the file.
func BenchmarkExport(b *testing.B) {
	items, targets := benchInputs(400, 500)
	for _, view := range []View{ViewImage, ViewFix, ViewCVE, ViewWorkload} {
		b.Run(string(view), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				resp, err := build(context.Background(), items, targets, view, Filter{}, true)
				if err != nil {
					b.Fatal(err)
				}
				body, err := WriteCSV(context.Background(), resp, Filter{}, "bench")
				if err != nil {
					b.Fatal(err)
				}
				b.ReportMetric(float64(len(body)), "bytes/file")
			}
		})
	}
}

// BenchmarkExposure is the per-read exposure join on a large namespace: 2,000
// Services (half exposing), 200 Ingresses and 5,000 Pods.
func BenchmarkExposure(b *testing.B) {
	var src routeSources
	for i := range 2000 {
		svc := service("big", fmt.Sprintf("svc-%d", i), corev1.ServiceTypeClusterIP, map[string]string{"app": fmt.Sprintf("app-%d", i%1000)})
		if i%2 == 0 {
			svc = withNodePort(service("big", fmt.Sprintf("svc-%d", i), corev1.ServiceTypeNodePort, map[string]string{"app": fmt.Sprintf("app-%d", i%1000)}))
		}
		src.services = append(src.services, svc)
	}
	for i := range 200 {
		src.ingresses = append(src.ingresses, ingress("big", fmt.Sprintf("ing-%d", i), fmt.Sprintf("svc-%d", i*2+1), true))
	}
	pods := make([]*corev1.Pod, 5000)
	for i := range pods {
		pods[i] = appPod("big", fmt.Sprintf("app-%d", i%1000))
	}
	b.ResetTimer()
	for b.Loop() {
		x := mustIndex(src, nil, nil)
		for _, p := range pods {
			_ = x.mustRoutes(p)
		}
	}
}
