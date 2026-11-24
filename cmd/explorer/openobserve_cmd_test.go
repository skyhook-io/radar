package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/openobserve/oofake"
	oo "github.com/skyhook-io/radar/pkg/prom/openobserve"
)

const (
	ooUIDB = "7b930c6c-dcbb-4d1f-9afd-67ea890873d7"
	ooUIDC = "dd1bfeff-e60c-4827-b8e0-b822ab22a9e8"
)

var ooSamplePods = []oo.PodRef{
	{Name: "web-1", UID: "11111111-1111-1111-1111-111111111111"},
	{Name: "web-2", UID: "22222222-2222-2222-2222-222222222222"},
}

func ooKube(uid string) *fake.Clientset {
	c := fake.NewClientset()
	_ = c.Tracker().Add(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: types.UID(uid)}})
	for _, p := range ooSamplePods {
		_ = c.Tracker().Add(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: p.Name, Namespace: "demo", UID: types.UID(p.UID)}, Status: corev1.PodStatus{Phase: corev1.PodRunning}})
	}
	_ = c.Tracker().Add(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "lonely", Namespace: "other", UID: "33333333-3333-3333-3333-333333333333"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}})
	return c
}

type ooRun struct {
	code     int
	out, err string
}

func runOO(t *testing.T, client kubernetes.Interface, stdin string, args ...string) ooRun {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd := openobserveCmd{
		in: strings.NewReader(stdin), out: &out, errOut: &errOut,
		kube: func(name string) (string, kubernetes.Interface, error) {
			if name == "" {
				name = "current-ctx"
			}
			return name, client, nil
		},
	}
	code := cmd.run(args)
	return ooRun{code, out.String(), errOut.String()}
}

func ooHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	return dir
}

func TestOpenObserveConnectMatchesByClusterID(t *testing.T) {
	home := ooHome(t)
	srv := oofake.Start(t, &oofake.Server{IDs: map[string]string{ooUIDB: "oo-b"}})
	r := runOO(t, ooKube(ooUIDB), "", "connect", "--url", srv.URL, "--user", "sa@example.com", "--token", "s3cret", "--context", "kind-oo-b")
	if r.code != 0 {
		t.Fatalf("exit %d\nout:\n%s\nerr:\n%s", r.code, r.out, r.err)
	}
	if !strings.Contains(r.out, `Context "kind-oo-b" is matched by cluster ID (radar_cluster_uid=`+ooUIDB+`)`) {
		t.Fatalf("out:\n%s", r.out)
	}
	entry := config.Load().OpenObserve
	if entry == nil || entry.Token != "s3cret" || entry.Org != "default" || entry.Matches[ooUIDB].By != config.OpenObserveMatchByID {
		t.Fatalf("saved = %+v", entry)
	}
	info, err := os.Stat(filepath.Join(home, ".radar", "config.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode: %v %v", info, err)
	}

	st := runOO(t, ooKube(ooUIDB), "", "status")
	if st.code != 0 || !strings.Contains(st.out, "matched by cluster ID") || strings.Contains(st.out, "s3cret") {
		t.Fatalf("status exit %d:\n%s%s", st.code, st.out, st.err)
	}

	d := runOO(t, nil, "", "disconnect")
	if d.code != 0 || config.Load().OpenObserve != nil {
		t.Fatalf("disconnect: %d %s", d.code, d.err)
	}
}

func TestOpenObserveConnectConfirmsSuggestedName(t *testing.T) {
	ooHome(t)
	srv := oofake.Start(t, &oofake.Server{PodCluster: "oo-c", Pods: ooSamplePods})
	r := runOO(t, ooKube(ooUIDC), "", "connect", "--url", srv.URL, "--user", "sa@example.com", "--token", "s3cret", "--context", "kind-oo-c", "--yes")
	if r.code != 0 {
		t.Fatalf("exit %d\nout:\n%s\nerr:\n%s", r.code, r.out, r.err)
	}
	if !strings.Contains(r.out, `matched by confirmed name (cluster=oo-c)`) || !strings.Contains(r.out, `radar_cluster_uid: "`+ooUIDC+`"`) {
		t.Fatalf("out:\n%s", r.out)
	}
	m := config.Load().OpenObserve.Matches[ooUIDC]
	if m.By != config.OpenObserveMatchByName || m.Label != "cluster" || m.Value != "oo-c" || m.ConfirmedAt == "" {
		t.Fatalf("saved match = %+v", m)
	}
}

func TestOpenObserveConnectAsksBeforeUsingAName(t *testing.T) {
	ooHome(t)
	srv := oofake.Start(t, &oofake.Server{PodCluster: "oo-c", Pods: ooSamplePods})
	r := runOO(t, ooKube(ooUIDC), "n\n", "connect", "--url", srv.URL, "--user", "sa@example.com", "--token", "s3cret")
	if r.code == 0 || !strings.Contains(r.out, "Use cluster=oo-c for context current-ctx? [y/N]") {
		t.Fatalf("exit %d\nout:\n%s", r.code, r.out)
	}
	if _, ok := config.Load().OpenObserve.Matches[ooUIDC]; ok {
		t.Fatal("a declined name must not be saved")
	}
	r = runOO(t, ooKube(ooUIDC), "y\n", "connect")
	if r.code != 0 || config.Load().OpenObserve.Matches[ooUIDC].Value != "oo-c" {
		t.Fatalf("reusing the saved connection: exit %d\n%s%s", r.code, r.out, r.err)
	}
}

func TestOpenObserveConnectRefusesTheDefaultClusterName(t *testing.T) {
	ooHome(t)
	srv := oofake.Start(t, &oofake.Server{PodCluster: "cluster1", Pods: ooSamplePods})
	r := runOO(t, ooKube(ooUIDC), "y\n", "connect", "--url", srv.URL, "--user", "sa@example.com", "--token", "s3cret", "--yes")
	if r.code == 0 {
		t.Fatalf("cluster1 must be refused:\n%s", r.out)
	}
	if !strings.Contains(r.out, `external_labels: { radar_cluster_uid: "`+ooUIDC+`" }`) {
		t.Fatalf("missing the ID-label line:\n%s", r.out)
	}
	if len(config.Load().OpenObserve.Matches) != 0 {
		t.Fatal("cluster1 must never be saved")
	}
}

func TestOpenObserveConnectNeverSendsASavedTokenToAnotherServer(t *testing.T) {
	ooHome(t)
	srv := oofake.Start(t, &oofake.Server{IDs: map[string]string{ooUIDB: "oo-b"}})
	if r := runOO(t, ooKube(ooUIDB), "", "connect", "--url", srv.URL, "--user", "sa@example.com", "--token", "s3cret"); r.code != 0 {
		t.Fatalf("%s%s", r.out, r.err)
	}
	r := runOO(t, ooKube(ooUIDB), "", "connect", "--url", "https://other.example.com")
	if r.code != 2 || !strings.Contains(r.err, "--token") {
		t.Fatalf("exit %d: %s", r.code, r.err)
	}
}
