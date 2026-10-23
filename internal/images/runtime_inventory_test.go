package images

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func runningPod(ns, name, node, workload string, containers map[string][2]string) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: map[string]string{"workload": workload}},
		Spec:       corev1.PodSpec{NodeName: node},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	for cname, img := range containers {
		pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{Name: cname, Image: img[0]})
		pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, corev1.ContainerStatus{Name: cname, ImageID: img[1]})
	}
	return pod
}

// podSource gives every Pod one pull secret, "regcred-<namespace>".
func podSource(pods ...*corev1.Pod) PodSource {
	src := podSourceWithoutSecrets(pods...)
	src.PullSecrets = func(p *corev1.Pod) []string { return []string{"regcred-" + p.Namespace} }
	return src
}

func podSourceWithoutSecrets(pods ...*corev1.Pod) PodSource {
	return PodSource{
		Pods:     pods,
		Workload: func(p *corev1.Pod) (string, string) { return "Deployment", p.Labels["workload"] },
		Platform: func(node string) (v1.Platform, bool) {
			switch node {
			case "arm-node":
				return v1.Platform{OS: "linux", Architecture: "arm64"}, true
			case "x86-node":
				return v1.Platform{OS: "linux", Architecture: "amd64"}, true
			}
			return v1.Platform{}, false
		},
	}
}

// fakeLookup answers by reference and credential: configs succeed for any
// credential not listed in errs.
type fakeLookup struct {
	calls    atomic.Int32
	mu       sync.Mutex
	configs  map[string]*v1.ConfigFile // by ref string
	errs     map[string]error          // by ref string + "|" + credential id
	seen     []string
	block    chan struct{}     // when set, lookups wait on it
	versions map[string]string // pull secret -> identity
}

func (f *fakeLookup) lookup(ctx context.Context, ref name.Reference, platform v1.Platform, keychain authn.Keychain) (*v1.ConfigFile, string, error) {
	f.calls.Add(1)
	cred := keychain.(credKeychain).id
	f.mu.Lock()
	f.seen = append(f.seen, ref.String()+" "+platform.Architecture+" "+cred)
	err, cfg := f.errs[ref.String()+"|"+cred], f.configs[ref.String()]
	f.mu.Unlock()
	if f.block != nil {
		<-f.block
	}
	if err != nil {
		return nil, "", err
	}
	if cfg != nil {
		return cfg, "sha256:resolved", nil
	}
	return nil, "", &transport.Error{StatusCode: http.StatusNotFound}
}

func (f *fakeLookup) seenCopy() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

type credKeychain struct{ id string }

func (credKeychain) Resolve(authn.Resource) (authn.Authenticator, error) { return authn.Anonymous, nil }

// fakeInventory gives every pull secret the identity in f.versions (default
// "rv1"), so ids read "<ns>/<secret>@rv1".
func fakeInventory(f *fakeLookup) *RuntimeInventory {
	return newRuntimeInventory(f.lookup, func(c credential, _ name.Reference) (authn.Keychain, string, bool) {
		if c.secret != "" && !c.radar {
			f.mu.Lock()
			c.identity = f.versions[c.namespace+"/"+c.secret]
			f.mu.Unlock()
			if c.identity == "" {
				c.identity = "rv1"
			}
		}
		return credKeychain{c.id()}, c.identity, true
	})
}

// refKey is how the fake keys a reference: the form lookup receives.
func refKey(t *testing.T, s string) string {
	t.Helper()
	ref, err := name.ParseReference(s)
	if err != nil {
		t.Fatal(err)
	}
	return ref.String()
}

func phpConfig(version string) *v1.ConfigFile {
	return &v1.ConfigFile{Config: v1.Config{Env: []string{"PHP_VERSION=" + version}}}
}

func unauthorized() error { return &transport.Error{StatusCode: http.StatusUnauthorized} }

const phpDigest = "docker.io/library/php@sha256:1111111111111111111111111111111111111111111111111111111111111111"

func TestInventory_GroupsByRunningDigestAndClassifies(t *testing.T) {
	f := &fakeLookup{configs: map[string]*v1.ConfigFile{
		refKey(t, phpDigest): phpConfig("8.3.10"),
		refKey(t, "node:20"): {Config: v1.Config{Env: []string{"NODE_VERSION=20.11.1"}}},
	}}
	inv := fakeInventory(f)
	src := podSource(
		runningPod("shop", "web-1", "x86-node", "web", map[string][2]string{"php": {"php:8.3-apache", phpDigest}}),
		runningPod("shop", "web-2", "x86-node", "web", map[string][2]string{"php": {"php:8.3-apache", phpDigest}}),
		runningPod("team-b", "cms-1", "x86-node", "cms", map[string][2]string{"app": {"php:8.3-apache", "docker-pullable://" + phpDigest}}),
		runningPod("shop", "api-1", "x86-node", "api", map[string][2]string{"node": {"node:20", "sha256:localimageid"}}),
	)
	got := inv.Inventory(context.Background(), src, InventoryOptions{Runtime: "php", FixedIn: []string{"8.2.35", "8.3.36"}})

	if got.Totals.Images != 2 || got.Totals.Matched != 1 || got.Totals.BelowFix != 1 || got.Totals.Unknown != 1 || len(got.Images) != 2 {
		t.Fatalf("totals = %+v, images = %+v", got.Totals, got.Images)
	}
	img := got.Images[0]
	if img.ResolvedFrom != "digest" || img.Status != StatusOK || img.Platform != "linux/amd64" || img.PlatformAssumed {
		t.Fatalf("image = %+v", img)
	}
	if len(img.Declared) != 1 || img.Declared[0].Fix != FixBelow || img.Declared[0].FixedIn != "8.3.36" {
		t.Fatalf("declared = %+v", img.Declared)
	}
	if img.WorkloadsTotal != 2 || img.Workloads[0].Name != "web" || img.Workloads[0].Pods != 2 || img.Workloads[1].Namespace != "team-b" {
		t.Fatalf("workloads = %+v", img.Workloads)
	}
}

func TestInventory_SpecDigestIsNotProofOfWhatRuns(t *testing.T) {
	f := &fakeLookup{configs: map[string]*v1.ConfigFile{refKey(t, phpDigest): phpConfig("8.3.36")}}
	got := fakeInventory(f).Inventory(context.Background(), podSource(
		runningPod("shop", "web-1", "x86-node", "web", map[string][2]string{"php": {phpDigest, "sha256:localimageid"}}),
	), InventoryOptions{Runtime: "php", FixedIn: []string{"8.3.36"}})
	img := got.Images[0]
	if img.ResolvedFrom != "spec" || img.Declared[0].Fix != "" {
		t.Fatalf("a spec digest the kubelet didn't report gets no verdict: %+v", img)
	}
}

func TestInventory_TagResolvedImagesGetNoFixVerdict(t *testing.T) {
	f := &fakeLookup{configs: map[string]*v1.ConfigFile{refKey(t, "php:8.3"): phpConfig("8.3.36")}}
	got := fakeInventory(f).Inventory(context.Background(), podSource(
		runningPod("shop", "web-1", "x86-node", "web", map[string][2]string{"php": {"php:8.3", "sha256:localimageid"}}),
	), InventoryOptions{Runtime: "php", FixedIn: []string{"8.3.36"}})
	img := got.Images[0]
	if img.ResolvedFrom != "tag" || len(img.Declared) != 1 || img.Declared[0].Version != "8.3.36" || img.Declared[0].Fix != "" {
		t.Fatalf("a tag's current image isn't proof of what runs: %+v", img)
	}
}

func TestInventory_UsesTheNodePlatformAndSaysWhenAssumed(t *testing.T) {
	f := &fakeLookup{configs: map[string]*v1.ConfigFile{}}
	got := fakeInventory(f).Inventory(context.Background(), podSourceWithoutSecrets(
		runningPod("shop", "web-1", "arm-node", "web", map[string][2]string{"php": {"php:8.3", phpDigest}}),
		runningPod("shop", "web-2", "unknown-node", "web", map[string][2]string{"php": {"php:8.3", phpDigest}}),
	), InventoryOptions{})
	joined := strings.Join(f.seenCopy(), ";")
	if f.calls.Load() != 2 || !strings.Contains(joined, " arm64 ") || !strings.Contains(joined, " amd64 ") {
		t.Fatalf("lookups = %v", f.seenCopy())
	}
	for _, img := range got.Images {
		if img.PlatformAssumed != (img.Platform == "linux/amd64") {
			t.Fatalf("an unreadable node platform must be flagged: %+v", img)
		}
	}
}

func TestInventory_PrivateResultsStayWithTheirCredentials(t *testing.T) {
	ref := refKey(t, phpDigest)
	f := &fakeLookup{
		configs: map[string]*v1.ConfigFile{ref: phpConfig("8.3.10")},
		errs: map[string]error{
			ref + "|shop/regcred-shop@rv1": unauthorized(),
			ref + "|anonymous":             unauthorized(),
		},
	}
	inv := fakeInventory(f)
	both := podSource(
		runningPod("shop", "web-1", "x86-node", "web", map[string][2]string{"php": {"php:8.3", phpDigest}}),
		runningPod("team-b", "cms-1", "x86-node", "cms", map[string][2]string{"php": {"php:8.3", phpDigest}}),
	)
	if got := inv.Inventory(context.Background(), both, InventoryOptions{}); got.Images[0].Status != StatusOK || f.calls.Load() != 2 {
		t.Fatalf("shop's secret fails, team-b's works: status %s after %v", got.Images[0].Status, f.seenCopy())
	}
	inv.Inventory(context.Background(), both, InventoryOptions{})
	if f.calls.Load() != 2 {
		t.Fatalf("a digest read with a credential is cached for it, got %v", f.seenCopy())
	}

	// A caller whose only visible Pod pulls with shop's secret must not get
	// what team-b's secret read.
	shopOnly := podSource(runningPod("shop", "web-1", "x86-node", "web", map[string][2]string{"php": {"php:8.3", phpDigest}}))
	got := inv.Inventory(context.Background(), shopOnly, InventoryOptions{})
	if got.Images[0].Status != StatusAuthRequired || len(got.Images[0].Declared) != 0 {
		t.Fatalf("another credential's success leaked: %+v after %v", got.Images[0], f.seenCopy())
	}
}

func TestInventory_RotatedSecretIsANewCredential(t *testing.T) {
	ref := refKey(t, phpDigest)
	f := &fakeLookup{
		configs: map[string]*v1.ConfigFile{ref: phpConfig("8.3.10")},
		errs:    map[string]error{ref + "|anonymous": unauthorized()},
	}
	inv := fakeInventory(f)
	src := podSource(runningPod("shop", "web-1", "x86-node", "web", map[string][2]string{"php": {"php:8.3", phpDigest}}))
	inv.Inventory(context.Background(), src, InventoryOptions{})
	calls := f.calls.Load()

	f.mu.Lock()
	f.versions = map[string]string{"shop/regcred-shop": "rv2"}
	f.errs[ref+"|shop/regcred-shop@rv2"] = unauthorized()
	f.mu.Unlock()
	got := inv.Inventory(context.Background(), src, InventoryOptions{})
	if got.Images[0].Status != StatusAuthRequired || f.calls.Load() == calls {
		t.Fatalf("a rotated secret must be checked again, not reuse the old success: %+v after %v", got.Images[0], f.seenCopy())
	}
}

func TestInventory_PublicResultsAnswerEveryone(t *testing.T) {
	ref := refKey(t, phpDigest)
	f := &fakeLookup{configs: map[string]*v1.ConfigFile{ref: phpConfig("8.3.10")}}
	inv := fakeInventory(f)
	inv.Inventory(context.Background(), podSourceWithoutSecrets(runningPod("shop", "web-1", "x86-node", "web", map[string][2]string{"php": {"php:8.3", phpDigest}})), InventoryOptions{})
	got := inv.Inventory(context.Background(), podSource(runningPod("team-b", "cms-1", "x86-node", "cms", map[string][2]string{"php": {"php:8.3", phpDigest}})), InventoryOptions{})
	if got.Images[0].Status != StatusOK || f.calls.Load() != 1 {
		t.Fatalf("an anonymous read is public: %+v after %v", got.Images[0], f.seenCopy())
	}
}

func TestInventory_TriesEachSecretOnItsOwnAndPastNotFound(t *testing.T) {
	ref := refKey(t, phpDigest)
	f := &fakeLookup{
		configs: map[string]*v1.ConfigFile{ref: phpConfig("8.3.10")},
		errs: map[string]error{
			ref + "|shop/stale@rv1": &transport.Error{StatusCode: http.StatusNotFound},
			ref + "|anonymous":      unauthorized(),
		},
	}
	src := podSourceWithoutSecrets(runningPod("shop", "web-1", "x86-node", "web", map[string][2]string{"php": {"php:8.3", phpDigest}}))
	src.PullSecrets = func(*corev1.Pod) []string { return []string{"stale", "valid"} }
	if got := fakeInventory(f).Inventory(context.Background(), src, InventoryOptions{}); got.Images[0].Status != StatusOK {
		t.Fatalf("a stale secret answering 404 must not hide a valid one: %+v after %v", got.Images[0], f.seenCopy())
	}
}

func TestInventory_RadarCredentialsOnlyWhenAllowed(t *testing.T) {
	ref := refKey(t, phpDigest)
	newLookup := func() *fakeLookup {
		return &fakeLookup{
			configs: map[string]*v1.ConfigFile{ref: phpConfig("8.3.10")},
			errs:    map[string]error{ref + "|anonymous": unauthorized()},
		}
	}
	src := podSourceWithoutSecrets(runningPod("shop", "web-1", "x86-node", "web", map[string][2]string{"php": {"php:8.3", phpDigest}}))

	f := newLookup()
	if got := fakeInventory(f).Inventory(context.Background(), src, InventoryOptions{}); got.Images[0].Status != StatusAuthRequired {
		t.Fatalf("with auth on, Radar's own credentials stay out: %+v after %v", got.Images[0], f.seenCopy())
	}
	f = newLookup()
	if got := fakeInventory(f).Inventory(context.Background(), src, InventoryOptions{RadarCredentials: true}); got.Images[0].Status != StatusOK {
		t.Fatalf("locally, Radar's credentials are the user's: %+v after %v", got.Images[0], f.seenCopy())
	}
}

func TestInventory_FailuresAreCachedThenRetried(t *testing.T) {
	ref := refKey(t, phpDigest)
	f := &fakeLookup{errs: map[string]error{ref + "|anonymous": unauthorized()}}
	inv := fakeInventory(f)
	now := time.Now()
	inv.now = func() time.Time { return now }
	src := podSourceWithoutSecrets(runningPod("shop", "web-1", "x86-node", "web", map[string][2]string{"php": {"php:8.3", phpDigest}}))

	if got := inv.Inventory(context.Background(), src, InventoryOptions{}); got.Images[0].Status != StatusAuthRequired {
		t.Fatalf("status = %s", got.Images[0].Status)
	}
	inv.Inventory(context.Background(), src, InventoryOptions{})
	if f.calls.Load() != 1 {
		t.Fatalf("a recent failure isn't retried, got %v", f.seenCopy())
	}
	now = now.Add(failureTTL + time.Second)
	inv.Inventory(context.Background(), src, InventoryOptions{})
	if f.calls.Load() != 2 {
		t.Fatalf("an expired failure is retried, got %v", f.seenCopy())
	}
}

func TestInventory_RateLimitStopsQueuedLookups(t *testing.T) {
	f := &fakeLookup{errs: map[string]error{}}
	var pods []*corev1.Pod
	for _, img := range []string{"php:8.1", "php:8.2", "php:8.3", "php:8.4"} {
		f.errs[refKey(t, img)+"|anonymous"] = &transport.Error{StatusCode: http.StatusTooManyRequests}
		pods = append(pods, runningPod("shop", "p-"+img, "x86-node", img, map[string][2]string{"c": {img, ""}}))
	}
	inv := fakeInventory(f)
	inv.slots = make(chan struct{}, 1)
	got := inv.Inventory(context.Background(), podSourceWithoutSecrets(pods...), InventoryOptions{})
	if got.Totals.RateLimited != 4 || f.calls.Load() != 1 {
		t.Fatalf("one 429 must stop every lookup queued for that registry: totals %+v, calls %v", got.Totals, f.seenCopy())
	}
}

func TestInventory_CapsLookupAttemptsPerCall(t *testing.T) {
	f := &fakeLookup{configs: map[string]*v1.ConfigFile{}}
	var pods []*corev1.Pod
	for i := 0; i < maxLookupsPerCall; i++ {
		img := "registry.example.com/app" + string(rune('a'+i%26)) + ":" + strings.Repeat("1", i/26+1)
		pods = append(pods, runningPod("shop", img, "x86-node", img, map[string][2]string{"c": {img, ""}}))
	}
	// Every image has a secret and anonymous to try, and both answer 404:
	// two attempts per image, so the budget runs out halfway.
	got := fakeInventory(f).Inventory(context.Background(), podSource(pods...), InventoryOptions{Limit: maxInventoryLimit})
	if int(f.calls.Load()) != maxLookupsPerCall || got.Totals.NotChecked == 0 {
		t.Fatalf("calls %d, totals %+v", f.calls.Load(), got.Totals)
	}
}

func TestInventory_HungLookupGivesItsSlotBack(t *testing.T) {
	f := &fakeLookup{configs: map[string]*v1.ConfigFile{}, block: make(chan struct{})}
	defer close(f.block)
	inv := fakeInventory(f)
	inv.slots = make(chan struct{}, 1)
	inv.fetchTimeout = 20 * time.Millisecond
	src := podSourceWithoutSecrets(
		runningPod("shop", "a", "x86-node", "a", map[string][2]string{"c": {"php:8.1", ""}}),
		runningPod("shop", "b", "x86-node", "b", map[string][2]string{"c": {"php:8.2", ""}}),
	)
	done := make(chan Inventory, 1)
	go func() { done <- inv.Inventory(context.Background(), src, InventoryOptions{}) }()
	select {
	case got := <-done:
		if f.calls.Load() != 2 {
			t.Fatalf("the second lookup never got the slot: %v, %+v", f.seenCopy(), got.Totals)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a hung lookup held the only slot")
	}
}

func TestInventory_SkipsFinishedPodsAndOneShotInitContainers(t *testing.T) {
	f := &fakeLookup{configs: map[string]*v1.ConfigFile{}}
	done := runningPod("shop", "job-1", "x86-node", "job", map[string][2]string{"c": {"php:8.1", ""}})
	done.Status.Phase = corev1.PodSucceeded
	always := corev1.ContainerRestartPolicyAlways
	web := runningPod("shop", "web-1", "x86-node", "web", map[string][2]string{"c": {"php:8.3", ""}})
	web.Spec.InitContainers = []corev1.Container{{Name: "migrate", Image: "php:8.2"}, {Name: "proxy", Image: "envoy:1", RestartPolicy: &always}}
	got := fakeInventory(f).Inventory(context.Background(), podSourceWithoutSecrets(done, web), InventoryOptions{})
	var images []string
	for _, img := range got.Images {
		images = append(images, img.Image)
	}
	if strings.Join(images, ",") != "envoy:1,php:8.3" && strings.Join(images, ",") != "php:8.3,envoy:1" {
		t.Fatalf("images = %v", images)
	}
}

// pushIndex pushes a two-platform index whose images declare different PHP
// versions, to an in-process registry.
func pushIndex(t *testing.T, host string) name.Reference {
	t.Helper()
	image := func(arch, version string) v1.Image {
		img, err := random.Image(64, 1)
		if err != nil {
			t.Fatal(err)
		}
		cf, err := img.ConfigFile()
		if err != nil {
			t.Fatal(err)
		}
		cf.OS, cf.Architecture = "linux", arch
		cf.Config.Env = []string{"PHP_VERSION=" + version, "DB_PASSWORD=hunter2"}
		img, err = mutate.ConfigFile(img, cf)
		if err != nil {
			t.Fatal(err)
		}
		return img
	}
	idx := mutate.AppendManifests(empty.Index,
		mutate.IndexAddendum{Add: image("amd64", "8.3.10"), Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "amd64"}}},
		mutate.IndexAddendum{Add: image("arm64", "8.3.11"), Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "arm64"}}},
	)
	ref, err := name.ParseReference(host+"/library/php:8.3", name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(ref, idx); err != nil {
		t.Fatal(err)
	}
	return ref
}

func newTestRegistry(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	return srv, strings.TrimPrefix(srv.URL, "http://")
}

func TestRegistryLookup_ReadsTheConfigForThePlatform(t *testing.T) {
	_, host := newTestRegistry(t)
	ref := pushIndex(t, host)

	lookup := registryConfigLookup(newGuardedTransport(false))
	for arch, want := range map[string]string{"amd64": "8.3.10", "arm64": "8.3.11"} {
		cfg, digest, err := lookup(context.Background(), ref, v1.Platform{OS: "linux", Architecture: arch}, authn.NewMultiKeychain())
		if err != nil {
			t.Fatal(err)
		}
		declared, _ := DeclaredRuntimes(cfg)
		if len(declared) != 1 || declared[0].Version != want || !strings.HasPrefix(digest, "sha256:") {
			t.Fatalf("%s: declared %+v digest %q", arch, declared, digest)
		}
	}
}

func TestRegistryLookup_InClusterRefusesLoopback(t *testing.T) {
	_, host := newTestRegistry(t)
	ref := pushIndex(t, host)

	_, _, err := registryConfigLookup(newGuardedTransport(true))(context.Background(), ref, v1.Platform{OS: "linux", Architecture: "amd64"}, authn.NewMultiKeychain())
	if classifyFetchError(err).status != StatusBlocked {
		t.Fatalf("err = %v, class = %+v", err, classifyFetchError(err))
	}
}

// A registry can redirect anywhere; only the registry the Pod named may sit
// on an internal address.
func TestRegistryLookup_RefusesRedirectsToOtherInternalHosts(t *testing.T) {
	var internalHits atomic.Int32
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		internalHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer internal.Close()
	internalURL, _ := url.Parse(internal.URL)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// localhost and 127.0.0.1 are different hosts to the guard.
		http.Redirect(w, r, "http://localhost:"+internalURL.Port()+"/admin", http.StatusFound)
	}))
	defer redirector.Close()
	ref, _ := name.ParseReference(strings.TrimPrefix(redirector.URL, "http://")+"/library/php:8.3", name.Insecure)

	_, _, err := registryConfigLookup(newGuardedTransport(false))(context.Background(), ref, v1.Platform{OS: "linux", Architecture: "amd64"}, authn.NewMultiKeychain())
	if err == nil || internalHits.Load() != 0 {
		t.Fatalf("the redirect reached an internal host: err %v, hits %d", err, internalHits.Load())
	}
}

func TestRegistryLookup_ChecksDestinationsBehindAProxy(t *testing.T) {
	var proxied atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxied.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	proxyURL, _ := url.Parse(proxy.URL)
	rt := newGuardedTransport(false)
	rt.proxy = func(*http.Request) (*url.URL, error) { return proxyURL, nil }

	metadata, _ := name.ParseReference("169.254.169.254/library/php:8.3", name.Insecure)
	_, _, err := registryConfigLookup(rt)(context.Background(), metadata, v1.Platform{OS: "linux", Architecture: "amd64"}, authn.NewMultiKeychain())
	if classifyFetchError(err).status != StatusBlocked || proxied.Load() != 0 {
		t.Fatalf("a link-local destination went to the proxy: err %v, proxied %d", err, proxied.Load())
	}

	public, _ := name.ParseReference("registry.example.com/library/php:8.3")
	_, _, err = registryConfigLookup(rt)(context.Background(), public, v1.Platform{OS: "linux", Architecture: "amd64"}, authn.NewMultiKeychain())
	if proxied.Load() == 0 || classifyFetchError(err).status != StatusError || strings.Contains(err.Error(), "allowed") {
		t.Fatalf("a public name the proxy resolves goes through it: err %v, proxied %d", err, proxied.Load())
	}
}

func TestRegistryLookup_DoesNotRetryRateLimits(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/v2/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	ref, _ := name.ParseReference(strings.TrimPrefix(srv.URL, "http://")+"/library/php:8.3", name.Insecure)

	_, _, err := registryConfigLookup(newGuardedTransport(false))(context.Background(), ref, v1.Platform{OS: "linux", Architecture: "amd64"}, authn.NewMultiKeychain())
	if classifyFetchError(err).status != StatusRateLimited {
		t.Fatalf("err = %v", err)
	}
	if n := hits.Load(); n > 2 {
		t.Fatalf("a 429 was retried: %d requests", n)
	}
}

func TestClassifyFetchError_DoesNotEchoResponseBodies(t *testing.T) {
	err := &transport.Error{StatusCode: http.StatusInternalServerError, Errors: []transport.Diagnostic{{Message: "internal admin page: secret=hunter2"}}}
	if got := classifyFetchError(err); got.status != StatusError || strings.Contains(got.err, "hunter2") {
		t.Fatalf("got %+v", got)
	}
}

func TestRegistryIPAllowed(t *testing.T) {
	cases := []struct {
		ip              string
		inCluster, name bool
		want            bool
	}{
		{"169.254.169.254", false, true, false},
		{"fe80::1", false, true, false},
		{"0.0.0.0", false, true, false},
		{"127.0.0.1", true, true, false},
		{"127.0.0.1", false, true, true},
		{"127.0.0.1", false, false, false},
		{"10.0.0.5", true, true, true},
		{"10.0.0.5", true, false, false},
		{"100.64.0.1", true, false, false},
		{"203.0.113.7", true, false, true},
	}
	for _, tc := range cases {
		if got := registryIPAllowed(net.ParseIP(tc.ip), tc.inCluster, tc.name); got != tc.want {
			t.Errorf("%s inCluster=%v named=%v: got %v", tc.ip, tc.inCluster, tc.name, got)
		}
	}
}

func TestCappedBody(t *testing.T) {
	read := func(n int) error {
		b := &cappedBody{ReadCloser: io.NopCloser(strings.NewReader(strings.Repeat("x", n))), left: 8}
		_, err := io.ReadAll(b)
		return err
	}
	if err := read(8); err != nil {
		t.Fatalf("a body at the cap: %v", err)
	}
	if err := read(9); err == nil {
		t.Fatal("a body over the cap must fail")
	}
}

// A connection a lookup opened to the registry it named must not serve
// another lookup's redirect to that address, nor may a named host's other
// ports inherit its exception.
func TestRegistryLookup_NamedExceptionIsPerLookupAndPort(t *testing.T) {
	var hits atomic.Int32
	reg := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	named := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		reg.ServeHTTP(w, r)
	}))
	defer named.Close()
	namedHost := strings.TrimPrefix(named.URL, "http://")
	ref := pushIndex(t, namedHost)

	rt := newGuardedTransport(false)
	lookup := registryConfigLookup(rt)
	if _, _, err := lookup(context.Background(), ref, v1.Platform{OS: "linux", Architecture: "amd64"}, authn.NewMultiKeychain()); err != nil {
		t.Fatal(err)
	}
	before := hits.Load()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, named.URL+"/v2/library/php/manifests/8.3", http.StatusFound)
	}))
	defer redirector.Close()
	other, _ := name.ParseReference(strings.TrimPrefix(redirector.URL, "http://")+"/library/php:8.3", name.Insecure)
	_, _, err := lookup(context.Background(), other, v1.Platform{OS: "linux", Architecture: "amd64"}, authn.NewMultiKeychain())
	if err == nil || hits.Load() != before {
		t.Fatalf("a redirect reached another lookup's internal registry: err %v, hits %d -> %d", err, before, hits.Load())
	}
}

func TestIsNamed(t *testing.T) {
	cases := []struct {
		registry, host, port string
		want                 bool
	}{
		{"registry.internal:5000", "registry.internal", "5000", true},
		{"registry.internal:5000", "registry.internal", "8080", false},
		{"registry.internal", "registry.internal", "443", true},
		{"registry.internal", "registry.internal", "8443", false},
		{"[fd00::1]", "fd00::1", "443", true},
		{"[fd00::1]:5000", "fd00::1", "5000", true},
		{"registry.internal", "other.internal", "443", false},
	}
	for _, tc := range cases {
		if got := isNamed(withRegistryHost(context.Background(), tc.registry), tc.host, tc.port); got != tc.want {
			t.Errorf("isNamed(%q, %s:%s) = %v", tc.registry, tc.host, tc.port, got)
		}
	}
}

func TestRegistryLookup_InClusterProxyFailsClosedOnUnresolvableNames(t *testing.T) {
	var proxied atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxied.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	proxyURL, _ := url.Parse(proxy.URL)
	rt := newGuardedTransport(true)
	rt.proxy = func(*http.Request) (*url.URL, error) { return proxyURL, nil }

	ref, _ := name.ParseReference("registry.does-not-resolve.invalid/library/php:8.3")
	_, _, err := registryConfigLookup(rt)(context.Background(), ref, v1.Platform{OS: "linux", Architecture: "amd64"}, authn.NewMultiKeychain())
	if classifyFetchError(err).status != StatusBlocked || proxied.Load() != 0 {
		t.Fatalf("in-cluster, a name the guard can't check must not reach the proxy: err %v, proxied %d", err, proxied.Load())
	}
}

func TestClassifyFetchError_DoesNotEchoTokenResponses(t *testing.T) {
	err := fmt.Errorf("GET https://evil.example/token: no token in bearer response:\n%s", `{"secret":"hunter2"}`)
	if got := classifyFetchError(err); strings.Contains(got.err, "hunter2") || got.status != StatusError {
		t.Fatalf("got %+v", got)
	}
}

func TestInventory_StuckLookupsAreNotCached(t *testing.T) {
	ref := refKey(t, phpDigest)
	f := &fakeLookup{configs: map[string]*v1.ConfigFile{ref: phpConfig("8.3.10")}}
	inv := fakeInventory(f)
	src := podSourceWithoutSecrets(runningPod("shop", "web-1", "x86-node", "web", map[string][2]string{"php": {"php:8.3", phpDigest}}))

	inv.outstanding.Store(maxOutstandingLookups)
	if got := inv.Inventory(context.Background(), src, InventoryOptions{}); got.Images[0].Status != StatusNotChecked || f.calls.Load() != 0 {
		t.Fatalf("with lookups stuck, new ones wait: %+v after %v", got.Images[0], f.seenCopy())
	}
	inv.outstanding.Store(0)
	if got := inv.Inventory(context.Background(), src, InventoryOptions{}); got.Images[0].Status != StatusOK {
		t.Fatalf("a not-checked result must not be cached: %+v", got.Images[0])
	}
}

func TestInventory_OneUnknownNodeMakesThePlatformAssumed(t *testing.T) {
	f := &fakeLookup{configs: map[string]*v1.ConfigFile{}}
	got := fakeInventory(f).Inventory(context.Background(), podSourceWithoutSecrets(
		runningPod("shop", "web-1", "x86-node", "web", map[string][2]string{"php": {"php:8.3", phpDigest}}),
		runningPod("shop", "web-2", "unknown-node", "web", map[string][2]string{"php": {"php:8.3", phpDigest}}),
	), InventoryOptions{})
	if len(got.Images) != 1 || !got.Images[0].PlatformAssumed {
		t.Fatalf("images = %+v", got.Images)
	}
}

// fakeConnectProxy accepts CONNECT tunnels, records each target and pipes
// to it.
func fakeConnectProxy(t *testing.T) (*url.URL, func() []string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var targets []string
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				req, err := http.ReadRequest(bufio.NewReader(c))
				if err != nil || req.Method != http.MethodConnect {
					return
				}
				mu.Lock()
				targets = append(targets, req.Host)
				mu.Unlock()
				up, err := net.Dial("tcp", req.Host)
				if err != nil {
					_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
					return
				}
				defer up.Close()
				_, _ = io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")
				go func() { _, _ = io.Copy(up, c) }()
				_, _ = io.Copy(c, up)
			}(c)
		}
	}()
	u, _ := url.Parse("http://" + ln.Addr().String())
	return u, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), targets...)
	}
}

// Through a proxy, the tunnel goes to the address that was checked, so the
// proxy never resolves the name itself.
func TestGuardedTransport_TunnelsToTheCheckedAddress(t *testing.T) {
	target, _ := net.Listen("tcp", "127.0.0.1:0")
	defer target.Close()
	go func() {
		for {
			c, err := target.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(target.Addr().String())
	proxyURL, targets := fakeConnectProxy(t)

	g := newGuardedTransport(false)
	ctx := context.WithValue(withRegistryHost(context.Background(), "localhost:"+port), proxyRouteKey{}, proxyURL)
	conn, err := g.dialContext(ctx, "tcp", "localhost:"+port)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	got := targets()
	if len(got) == 0 {
		t.Fatal("no CONNECT reached the proxy")
	}
	for _, target := range got {
		if host, _, _ := net.SplitHostPort(target); net.ParseIP(host) == nil {
			t.Fatalf("CONNECT targets = %v, want resolved addresses, never the name", got)
		}
	}
}

func TestGuardedTransport_InClusterRefusesUnpinnableProxyRequests(t *testing.T) {
	proxyURL, targets := fakeConnectProxy(t)
	g := newGuardedTransport(true)
	g.proxy = func(*http.Request) (*url.URL, error) { return proxyURL, nil }
	req, _ := http.NewRequest(http.MethodGet, "http://203.0.113.7/v2/", nil)
	if _, err := g.RoundTrip(req); !isBlocked(err) || len(targets()) != 0 {
		t.Fatalf("plain HTTP through a proxy can't be pinned in-cluster: err %v, proxy saw %v", err, targets())
	}
}

func TestInventory_GroupsSpellingsOfOneDigestAndTrustsTheKubelet(t *testing.T) {
	short := "php@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	f := &fakeLookup{configs: map[string]*v1.ConfigFile{refKey(t, short): phpConfig("8.3.10"), refKey(t, phpDigest): phpConfig("8.3.10")}}
	got := fakeInventory(f).Inventory(context.Background(), podSourceWithoutSecrets(
		runningPod("shop", "web-1", "x86-node", "web", map[string][2]string{"php": {short, "sha256:localimageid"}}),
		runningPod("shop", "web-2", "x86-node", "web", map[string][2]string{"php": {short, phpDigest}}),
	), InventoryOptions{Runtime: "php", FixedIn: []string{"8.3.36"}})
	if len(got.Images) != 1 || got.Images[0].ResolvedFrom != "digest" || got.Images[0].Declared[0].Fix != FixBelow {
		t.Fatalf("one digest, verified by the kubelet for one Pod: %+v", got.Images)
	}
}

func TestInventory_ManySecretsStillTryAnonymously(t *testing.T) {
	ref := refKey(t, phpDigest)
	f := &fakeLookup{configs: map[string]*v1.ConfigFile{ref: phpConfig("8.3.10")}, errs: map[string]error{}}
	src := podSourceWithoutSecrets(runningPod("shop", "web-1", "x86-node", "web", map[string][2]string{"php": {"php:8.3", phpDigest}}))
	src.PullSecrets = func(*corev1.Pod) []string { return []string{"s1", "s2", "s3", "s4", "s5"} }
	for _, s := range []string{"s1", "s2", "s3", "s4", "s5"} {
		f.errs[ref+"|shop/"+s+"@rv1"] = unauthorized()
	}
	got := fakeInventory(f).Inventory(context.Background(), src, InventoryOptions{RadarCredentials: true})
	if got.Images[0].Status != StatusOK || !strings.Contains(strings.Join(f.seenCopy(), ";"), " anonymous") {
		t.Fatalf("a public image must still be tried anonymously: %+v after %v", got.Images[0], f.seenCopy())
	}
}

func TestInventory_IncludesEphemeralContainers(t *testing.T) {
	f := &fakeLookup{configs: map[string]*v1.ConfigFile{}}
	pod := runningPod("shop", "web-1", "x86-node", "web", map[string][2]string{"php": {"php:8.3", ""}})
	pod.Spec.EphemeralContainers = []corev1.EphemeralContainer{
		{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug", Image: "busybox:1.36"}},
		{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "old-debug", Image: "php:8.1"}},
	}
	pod.Status.EphemeralContainerStatuses = []corev1.ContainerStatus{
		{Name: "debug", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
		{Name: "old-debug", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}}},
	}
	got := fakeInventory(f).Inventory(context.Background(), podSourceWithoutSecrets(pod), InventoryOptions{})
	var images []string
	for _, img := range got.Images {
		images = append(images, img.Image)
	}
	if got.Totals.Images != 2 || strings.Contains(strings.Join(images, ","), "php:8.1") {
		t.Fatalf("a running debug container counts, an exited one doesn't: %v", images)
	}
}

func TestConnectVia_GivesUpOnASilentProxy(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close() // accept, never answer
		}
	}()
	proxyURL, _ := url.Parse("http://" + ln.Addr().String())
	g := newGuardedTransport(false)
	g.connectTimeout = 100 * time.Millisecond
	start := time.Now()
	if _, err := g.connectVia(context.WithoutCancel(context.Background()), proxyURL, "203.0.113.7:443"); err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("err %v after %v", err, time.Since(start))
	}
}

func TestConnectVia_IgnoresFramingOnASuccessfulTunnel(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = http.ReadRequest(bufio.NewReader(c))
		_, _ = io.WriteString(c, "HTTP/1.1 200 Connection established\r\nTransfer-Encoding: chunked\r\n\r\n")
		time.Sleep(2 * time.Second) // waiting for the client's TLS hello
	}()
	proxyURL, _ := url.Parse("http://" + ln.Addr().String())
	g := newGuardedTransport(false)
	start := time.Now()
	conn, err := g.connectVia(context.Background(), proxyURL, "203.0.113.7:443")
	if err != nil || time.Since(start) > time.Second {
		t.Fatalf("a 200 tunnel must be usable at once: err %v after %v", err, time.Since(start))
	}
	conn.Close()
}

func TestInventory_MissingRuntimeDeclarationStaysUnknown(t *testing.T) {
	for _, runtime := range []string{"php", ""} {
		t.Run(runtime, func(t *testing.T) {
			f := &fakeLookup{configs: map[string]*v1.ConfigFile{refKey(t, phpDigest): {}}}
			src := podSourceWithoutSecrets(runningPod("shop", "web", "x86-node", "web", map[string][2]string{"app": {"php:8.3", phpDigest}}))
			got := fakeInventory(f).Inventory(context.Background(), src, InventoryOptions{Runtime: runtime})
			if len(got.Images) != 1 || got.Totals.Unknown != 1 || got.Totals.Matched != 0 || got.Images[0].Status != StatusNoDeclaration {
				t.Fatalf("missing declaration must remain visible and unknown: %+v", got)
			}
		})
	}
}

func TestInventory_AssumedPlatformGetsNoFixVerdict(t *testing.T) {
	f := &fakeLookup{configs: map[string]*v1.ConfigFile{refKey(t, phpDigest): phpConfig("8.3.36")}}
	src := podSourceWithoutSecrets(runningPod("shop", "web", "unknown-node", "web", map[string][2]string{"app": {"php:8.3", phpDigest}}))
	got := fakeInventory(f).Inventory(context.Background(), src, InventoryOptions{Runtime: "php", FixedIn: []string{"8.3.36"}})
	img := got.Images[0]
	if !img.PlatformAssumed || img.Declared[0].Fix != "" || got.Totals.BelowFix != 0 {
		t.Fatalf("assumed platform must not receive a verdict: %+v", img)
	}
}

type runtimeCredentialKeychain struct{}

func (runtimeCredentialKeychain) Resolve(authn.Resource) (authn.Authenticator, error) {
	return authn.FromConfig(authn.AuthConfig{Username: "test", Password: "dummy"}), nil
}

func TestRegistryLookup_CredentialsNeverUseHTTP(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	ref, err := name.ParseReference(strings.TrimPrefix(server.URL, "http://") + "/app:latest")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = registryConfigLookup(newGuardedTransport(false))(context.Background(), ref, v1.Platform{OS: "linux", Architecture: "amd64"}, runtimeCredentialKeychain{})
	if err == nil || !isBlocked(err) || requests.Load() != 0 {
		t.Fatalf("credentialed lookup must refuse HTTP before sending requests: err=%v requests=%d", err, requests.Load())
	}
}

func TestInventory_TransientFailuresExpireQuickly(t *testing.T) {
	f := &fakeLookup{errs: map[string]error{refKey(t, phpDigest) + "|anonymous": &transport.Error{StatusCode: http.StatusServiceUnavailable}}}
	inv := fakeInventory(f)
	now := time.Now()
	inv.now = func() time.Time { return now }
	src := podSourceWithoutSecrets(runningPod("shop", "web", "x86-node", "web", map[string][2]string{"app": {"php:8.3", phpDigest}}))
	inv.Inventory(context.Background(), src, InventoryOptions{})
	inv.Inventory(context.Background(), src, InventoryOptions{})
	if f.calls.Load() != 1 {
		t.Fatalf("failure was not cached: %d", f.calls.Load())
	}
	now = now.Add(transientFailureTTL + time.Second)
	inv.Inventory(context.Background(), src, InventoryOptions{})
	if f.calls.Load() != 2 {
		t.Fatalf("transient failure did not expire: %d", f.calls.Load())
	}
}

func TestClassifyFetchError_AuthDoesNotBlamePodCredentials(t *testing.T) {
	got := classifyFetchError(unauthorized())
	if got.status != StatusAuthRequired || strings.Contains(got.err, "Pod would pull") || !strings.Contains(got.err, "node credentials were not used") {
		t.Fatalf("misleading authorization failure: %+v", got)
	}
}

func TestInventory_PartialCredentialBudgetIsNotChecked(t *testing.T) {
	f := &fakeLookup{errs: map[string]error{refKey(t, phpDigest) + "|shop/regcred-shop@rv1": unauthorized()}}
	inv := fakeInventory(f)
	src := podSource(runningPod("shop", "web", "x86-node", "web", map[string][2]string{"app": {"php:8.3", phpDigest}}))
	w := collectWork(src)[0]
	budget := new(atomic.Int32)
	budget.Store(1)
	got := inv.fetch(context.Background(), w, inv.credentials(w, false), budget)
	if got.status != StatusNotChecked || f.calls.Load() != 1 {
		t.Fatalf("unfinished credential attempts must be not_checked: %+v calls=%d", got, f.calls.Load())
	}
}
