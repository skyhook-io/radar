package main

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/skyhook-io/radar/internal/k8s"
)

// The -vulnerability-reports fixture: workloads in shop and payments, and
// VulnerabilityReports shaped the way Trivy Operator writes them (controller
// owner reference, trivy-operator.* labels, artifact/registry/os, findings)
// about them. It covers what the Vulnerabilities views must tell apart: one
// image run by workloads in two namespaces, a sidecar, a StatefulSet mid-
// rollout on two images, a CronJob between runs, a bare Pod, an older
// ReplicaSet's leftover report, a running image with no report yet, and an
// image scanned with no findings.

var vulnerabilityReportGVR = schema.GroupVersionResource{Group: "aquasecurity.github.io", Version: "v1alpha1", Resource: "vulnerabilityreports"}

type fixtureFinding struct {
	id, severity, pkg, installed, fixed, title string
	score                                      float64
}

type fixtureVuln struct {
	fixtureFinding
	pkgType string
}

func ofType(pkgType string, findings []fixtureFinding) []fixtureVuln {
	out := make([]fixtureVuln, len(findings))
	for i, f := range findings {
		out[i] = fixtureVuln{f, pkgType}
	}
	return out
}

var (
	debianBase = ofType("debian", []fixtureFinding{
		{"CVE-2026-20871", "CRITICAL", "zlib1g", "1:1.2.13.dfsg-1", "1:1.2.13.dfsg-1+deb12u1", "zlib: heap overflow in inflate on a crafted stream", 9.8},
		{"CVE-2026-31337", "CRITICAL", "libexpat1", "2.5.0-1", "2.5.0-1+deb12u2", "expat: integer overflow in XML_GetBuffer", 9.1},
		{"CVE-2026-11902", "HIGH", "libssl3", "3.0.11-1~deb12u2", "3.0.15-1~deb12u1", "openssl: timing side channel in RSA decryption", 7.5},
		{"CVE-2026-08455", "HIGH", "libcurl4", "7.88.1-10+deb12u5", "7.88.1-10+deb12u8", "curl: use after free in connection reuse", 7.5},
		{"CVE-2026-40012", "HIGH", "libsystemd0", "252.22-1~deb12u1", "", "systemd: out-of-bounds read in journal parsing", 7.1},
		{"CVE-2026-17733", "MEDIUM", "tar", "1.34+dfsg-1.2", "", "tar: path traversal with crafted archive names", 5.5},
		{"CVE-2026-29004", "MEDIUM", "libgnutls30", "3.7.9-2+deb12u2", "3.7.9-2+deb12u3", "gnutls: timing side channel in RSA-PSK", 5.3},
		{"CVE-2026-05518", "LOW", "bash", "5.2.15-2+b2", "", "bash: unchecked return in history expansion", 3.3},
		{"CVE-2026-05519", "LOW", "coreutils", "9.1-1", "", "coreutils: heap overread in split", 2.8},
	})
	alpineBase = ofType("alpine", []fixtureFinding{
		{"CVE-2026-17733", "HIGH", "musl", "1.2.4-r2", "1.2.4-r3", "musl: buffer overflow in iconv GB18030", 7.5},
		{"CVE-2026-23001", "MEDIUM", "busybox", "1.36.1-r15", "1.36.1-r17", "busybox: use after free in awk", 6.5},
	})
	goApp = ofType("gobinary", []fixtureFinding{
		{"CVE-2026-44120", "CRITICAL", "golang.org/x/net", "v0.31.0", "v0.38.0, v0.36.1", "x/net/http2: unbounded memory growth from HPACK headers", 9.0},
		{"CVE-2026-44177", "MEDIUM", "golang.org/x/net", "v0.31.0", "v0.33.0", "x/net/html: quadratic parsing of nested tags", 5.3},
		{"CVE-2026-29118", "HIGH", "stdlib", "1.23.2", "1.23.8", "net/http: request smuggling via chunked encoding", 7.5},
	})
)

func vulnFixture(base ...[]fixtureVuln) []fixtureVuln {
	var out []fixtureVuln
	for _, b := range base {
		out = append(out, b...)
	}
	return out
}

func fixtureReport(ns, ownerKind, ownerAPIVersion, ownerName, ownerUID, container, registry, repo, tag, digest, osFamily, osName, scanned string, vulns []fixtureVuln) *unstructured.Unstructured {
	counts := map[string]int64{}
	items := make([]any, 0, len(vulns))
	for _, v := range vulns {
		counts[v.severity]++
		items = append(items, map[string]any{
			"vulnerabilityID": v.id, "severity": v.severity, "resource": v.pkg, "installedVersion": v.installed,
			"fixedVersion": v.fixed, "title": v.title, "score": v.score, "packageType": v.pkgType,
			"primaryLink": "https://avd.aquasec.com/nvd/" + v.id,
		})
	}
	u := &unstructured.Unstructured{Object: map[string]any{
		"report": map[string]any{
			"registry": map[string]any{"server": registry},
			"artifact": map[string]any{"repository": repo, "tag": tag, "digest": digest},
			"os":       map[string]any{"family": osFamily, "name": osName},
			"scanner":  map[string]any{"name": "Trivy", "vendor": "Aqua Security", "version": "0.58.1"},
			"summary": map[string]any{
				"criticalCount": counts["CRITICAL"], "highCount": counts["HIGH"], "mediumCount": counts["MEDIUM"],
				"lowCount": counts["LOW"], "unknownCount": counts["UNKNOWN"], "noneCount": int64(0),
			},
			"updateTimestamp": scanned,
			"vulnerabilities": items,
		},
	}}
	u.SetAPIVersion("aquasecurity.github.io/v1alpha1")
	u.SetKind("VulnerabilityReport")
	u.SetNamespace(ns)
	u.SetName(fmt.Sprintf("%s-%s-%s", lower(ownerKind), ownerName, container))
	u.SetLabels(map[string]string{
		"trivy-operator.resource.kind":      ownerKind,
		"trivy-operator.resource.name":      ownerName,
		"trivy-operator.resource.namespace": ns,
		"trivy-operator.container.name":     container,
		"app.kubernetes.io/managed-by":      "trivy-operator",
	})
	controller := true
	u.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: ownerAPIVersion, Kind: ownerKind, Name: ownerName, UID: types.UID(ownerUID), Controller: &controller}})
	return u
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

func fixturePod(ns, name, ownerKind, ownerAPIVersion, ownerName, ownerUID string, containers ...corev1.Container) *corev1.Pod {
	controller := true
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID("uid-" + ns + "-" + name)},
		Spec:       corev1.PodSpec{Containers: containers},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if ownerKind != "" {
		p.OwnerReferences = []metav1.OwnerReference{{APIVersion: ownerAPIVersion, Kind: ownerKind, Name: ownerName, UID: types.UID(ownerUID), Controller: &controller}}
	}
	return p
}

func fixtureDeployment(ns, name, rs string, replicas int32, containers ...corev1.Container) []runtime.Object {
	controller := true
	depUID, rsUID := "uid-"+ns+"-"+name, "uid-"+ns+"-"+rs
	labels := map[string]string{"app": name}
	template := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Containers: containers}}
	objs := []runtime.Object{
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID(depUID)},
			Spec:       appsv1.DeploymentSpec{Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: template},
			Status:     appsv1.DeploymentStatus{Replicas: replicas, ReadyReplicas: replicas, AvailableReplicas: replicas, UpdatedReplicas: replicas},
		},
		&appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{Name: rs, Namespace: ns, UID: types.UID(rsUID), Labels: labels,
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: name, UID: types.UID(depUID), Controller: &controller}}},
			Spec:   appsv1.ReplicaSetSpec{Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: template},
			Status: appsv1.ReplicaSetStatus{Replicas: replicas, ReadyReplicas: replicas},
		},
	}
	for i := int32(0); i < replicas; i++ {
		pod := fixturePod(ns, fmt.Sprintf("%s-%d", rs, i), "ReplicaSet", "apps/v1", rs, rsUID, containers...)
		pod.Labels = labels
		objs = append(objs, pod)
	}
	return objs
}

// vulnerabilityFixture returns the typed objects to add to the fake client and
// the reports for the dynamic one.
func vulnerabilityFixture() (typed []runtime.Object, reports []runtime.Object) {
	const (
		checkoutDigest = "sha256:9f2c4be1d07a6c3f0e8a2b5d14c7a9e3f6b8d2c1a0e4f7b9c3d5e8a1f2b4c6a41e"
		nginxDigest    = "sha256:5e1b8a77c0d4e2f9b3a6c1d8e7f2a9b4c3d6e1f8a2b7c4d9e3f6a1b8c5d2e77c0"
		pg164Digest    = "sha256:c08d2e4f6a8b1c3d5e7f9a2b4c6d8e1f3a5b7c9d2e4f6a8b1c3d5e7f9a21f9a0"
		agentDigest    = "sha256:aa71c3d5e7f9b2d4f6a8c1e3b5d7f9a2c4e6b8d1f3a5c7e9b2d4f6a8c1e30c3d"
		reportDigest   = "sha256:7d03b6e1a4c7f2d5b8e3a6c9f1d4b7e2a5c8f3d6b9e1a4c7f2d5b8e3a6c9b6e1"
		gatewayDigest  = "sha256:31aae902b4d6f8a1c3e5b7d9f2a4c6e8b1d3f5a7c9e2b4d6f8a1c3e5b7d9e902"
	)
	checkout := corev1.Container{Name: "app", Image: "ghcr.io/acme/checkout:2.14.0"}
	envoy := corev1.Container{Name: "envoy", Image: "envoyproxy/envoy:v1.31.2"}
	nginx := corev1.Container{Name: "nginx", Image: "nginx:1.23.4"}

	typed = append(typed,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "payments"}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}},
	)
	typed = append(typed, fixtureDeployment("shop", "checkout", "checkout-7d9f8c", 3, checkout, envoy)...)
	typed = append(typed, fixtureDeployment("shop", "storefront", "storefront-5b6c7d", 2, nginx)...)
	typed = append(typed, fixtureDeployment("shop", "api-gateway", "api-gateway-6c8d9e", 2, corev1.Container{Name: "gateway", Image: "ghcr.io/acme/api-gateway:7.3.1"})...)
	typed = append(typed, fixtureDeployment("payments", "billing", "billing-4f5a6b", 2, corev1.Container{Name: "app", Image: "ghcr.io/acme/checkout:2.14.0"})...)
	// queue-keeper: an image scanned with no findings.
	typed = append(typed, fixtureDeployment("shop", "queue-keeper", "queue-keeper-2d3e4f", 1, corev1.Container{Name: "keeper", Image: "registry.k8s.io/pause:3.9"})...)

	// An older checkout ReplicaSet, scaled to zero; its report is a leftover.
	controller := true
	zero := int32(0)
	typed = append(typed, &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-6a1b2c", Namespace: "shop", UID: "uid-shop-checkout-6a1b2c",
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "checkout", UID: "uid-shop-checkout", Controller: &controller}}},
		Spec: appsv1.ReplicaSetSpec{Replicas: &zero},
	})

	// db: a StatefulSet mid-rollout. Its report is about postgres:16.4; the
	// Pod still on 16.3 has none.
	dbLabels := map[string]string{"app": "db"}
	typed = append(typed,
		&appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "shop", UID: "uid-shop-db"},
			Spec: appsv1.StatefulSetSpec{Selector: &metav1.LabelSelector{MatchLabels: dbLabels}, Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: dbLabels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "postgres", Image: "postgres:16.4"}}}}},
		},
		fixturePod("shop", "db-0", "StatefulSet", "apps/v1", "db", "uid-shop-db", corev1.Container{Name: "postgres", Image: "postgres:16.3"}),
		fixturePod("shop", "db-1", "StatefulSet", "apps/v1", "db", "uid-shop-db", corev1.Container{Name: "postgres", Image: "postgres:16.4"}),
	)

	// node-agent: a DaemonSet on two nodes, scanned a while ago.
	agentLabels := map[string]string{"app": "node-agent"}
	typed = append(typed,
		&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: "node-agent", Namespace: "shop", UID: "uid-shop-node-agent"},
			Spec: appsv1.DaemonSetSpec{Selector: &metav1.LabelSelector{MatchLabels: agentLabels}, Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: agentLabels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "agent", Image: "quay.io/prometheus/node-exporter:v1.5.0"}}}}},
		},
		fixturePod("shop", "node-agent-x1", "DaemonSet", "apps/v1", "node-agent", "uid-shop-node-agent", corev1.Container{Name: "agent", Image: "quay.io/prometheus/node-exporter:v1.5.0"}),
		fixturePod("shop", "node-agent-x2", "DaemonSet", "apps/v1", "node-agent", "uid-shop-node-agent", corev1.Container{Name: "agent", Image: "quay.io/prometheus/node-exporter:v1.5.0"}),
	)

	// nightly-report: a CronJob between runs (no Pod).
	nightly := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "nightly-report", Namespace: "shop", UID: "uid-shop-nightly-report"}, Spec: batchv1.CronJobSpec{Schedule: "0 2 * * *"}}
	nightly.Spec.JobTemplate.Spec.Template.Spec.Containers = []corev1.Container{{Name: "report", Image: "ghcr.io/acme/nightly-report:0.9.2"}}
	nightly.Spec.JobTemplate.Spec.Template.Spec.RestartPolicy = corev1.RestartPolicyOnFailure
	typed = append(typed, nightly)

	// debug: a bare Pod.
	typed = append(typed, fixturePod("shop", "debug", "", "", "", "", corev1.Container{Name: "shell", Image: "busybox:1.36"}))

	reports = []runtime.Object{
		fixtureReport("shop", "ReplicaSet", "apps/v1", "checkout-7d9f8c", "uid-shop-checkout-7d9f8c", "app", "ghcr.io", "acme/checkout", "2.14.0", checkoutDigest, "debian", "12.7", "2026-11-15T09:40:00Z", vulnFixture(debianBase, goApp)),
		fixtureReport("payments", "ReplicaSet", "apps/v1", "billing-4f5a6b", "uid-payments-billing-4f5a6b", "app", "ghcr.io", "acme/checkout", "2.14.0", checkoutDigest, "debian", "12.7", "2026-11-15T08:10:00Z", vulnFixture(debianBase, goApp)),
		fixtureReport("shop", "ReplicaSet", "apps/v1", "checkout-7d9f8c", "uid-shop-checkout-7d9f8c", "envoy", "index.docker.io", "envoyproxy/envoy", "v1.31.2", "sha256:e1f0c2d4a6b8c1e3f5a7b9d2c4e6f8a1b3d5c7e9f2a4b6d8c1e3f5a7b9d2e0f0", "ubuntu", "22.04", "2026-11-15T09:41:00Z", ofType("ubuntu", []fixtureFinding{
			{"CVE-2026-11902", "HIGH", "libssl3", "3.0.2-0ubuntu1.15", "3.0.2-0ubuntu1.18", "openssl: timing side channel in RSA decryption", 7.5},
			{"CVE-2026-17733", "MEDIUM", "tar", "1.34+dfsg-1ubuntu0.1.22.04.1", "", "tar: path traversal with crafted archive names", 5.5},
		})),
		fixtureReport("shop", "ReplicaSet", "apps/v1", "storefront-5b6c7d", "uid-shop-storefront-5b6c7d", "nginx", "index.docker.io", "library/nginx", "1.23.4", nginxDigest, "debian", "11.6", "2026-11-15T07:55:00Z", vulnFixture(debianBase[:6])),
		fixtureReport("shop", "StatefulSet", "apps/v1", "db", "uid-shop-db", "postgres", "index.docker.io", "library/postgres", "16.4", pg164Digest, "debian", "12.7", "2026-11-15T06:00:00Z", vulnFixture(debianBase[2:5], ofType("debian", []fixtureFinding{
			{"CVE-2026-37210", "HIGH", "postgresql-16", "16.4-1.pgdg120+1", "16.6-1.pgdg120+1", "postgresql: SQL injection via quoting in libpq", 8.1},
		}))),
		fixtureReport("shop", "DaemonSet", "apps/v1", "node-agent", "uid-shop-node-agent", "agent", "quay.io", "prometheus/node-exporter", "v1.5.0", agentDigest, "", "", "2026-11-13T22:00:00Z", goApp),
		fixtureReport("shop", "CronJob", "batch/v1", "nightly-report", "uid-shop-nightly-report", "report", "ghcr.io", "acme/nightly-report", "0.9.2", reportDigest, "alpine", "3.19.1", "2026-11-15T02:05:00Z", alpineBase),
		fixtureReport("shop", "ReplicaSet", "apps/v1", "api-gateway-6c8d9e", "uid-shop-api-gateway-6c8d9e", "gateway", "ghcr.io", "acme/api-gateway", "7.3.1", gatewayDigest, "alpine", "3.20.3", "2026-11-15T09:30:00Z", vulnFixture(alpineBase, goApp[2:])),
		fixtureReport("shop", "ReplicaSet", "apps/v1", "queue-keeper-2d3e4f", "uid-shop-queue-keeper-2d3e4f", "keeper", "registry.k8s.io", "pause", "3.9", "sha256:7031c1b283388d2c2e09b57badb803c05ebed362dc88d84b480cc47f72a21097", "", "", "2026-11-15T09:00:00Z", nil),
		// Leftover from the older checkout revision: not counted.
		fixtureReport("shop", "ReplicaSet", "apps/v1", "checkout-6a1b2c", "uid-shop-checkout-6a1b2c", "app", "ghcr.io", "acme/checkout", "2.13.0", "sha256:0ld", "debian", "12.5", "2026-11-01T09:40:00Z", debianBase),
	}
	return typed, reports
}

// initVulnerabilityReports serves the fixture's reports through the dynamic
// cache, as discovery would for a cluster with Trivy Operator installed.
func initVulnerabilityReports(reports []runtime.Object) error {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		vulnerabilityReportGVR: "VulnerabilityReportList",
		// /api/resource-counts probes EndpointSlices through the dynamic client.
		{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}: "EndpointSliceList",
	}, reports...)
	return k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{
		Group: vulnerabilityReportGVR.Group, Version: vulnerabilityReportGVR.Version, Kind: "VulnerabilityReport", Name: vulnerabilityReportGVR.Resource,
		Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"},
	}})
}
