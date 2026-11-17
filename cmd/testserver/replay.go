package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/clusterreplay"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	discoveryfake "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
)

var replayPath = flag.String("replay", "", "replay a frozen .ndjson.gz cluster capture")

type replayState struct {
	client       *fake.Clientset
	dynamic      *dynamicfake.FakeDynamicClient
	discovery    *replayDiscovery
	gvrs         []schema.GroupVersionResource
	unsupported  []string
	failedGroups []string
	skipped      []clusterreplay.Skipped
	objects      int
}

func loadReplay(path string) (*replayState, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var objects []*unstructured.Unstructured
	header, err := clusterreplay.Read(f, func(obj *unstructured.Unstructured) error { objects = append(objects, obj); return nil })
	if err != nil {
		return nil, err
	}
	s := &replayState{client: fake.NewClientset(), objects: len(objects), failedGroups: header.FailedGroups, skipped: header.Skipped}
	s.discovery = &replayDiscovery{FakeDiscovery: s.client.Discovery().(*discoveryfake.FakeDiscovery), groups: header.Discovery.Groups}
	s.discovery.Resources = header.Discovery.Resources
	kinds := map[schema.GroupVersionResource]string{}
	byGVK := map[schema.GroupVersionKind]schema.GroupVersionResource{}
	for _, list := range header.Discovery.Resources {
		gv, err := schema.ParseGroupVersion(list.GroupVersion)
		if err != nil {
			return nil, err
		}
		for _, r := range list.APIResources {
			if !clusterreplay.Listable(gv, r) {
				continue
			}
			gvr := gv.WithResource(r.Name)
			kinds[gvr] = r.Kind + "List"
			byGVK[gv.WithKind(r.Kind)] = gvr
			// Production serves built-in kinds from typed informers and starts
			// dynamic ones only for custom resources and DRA, so replay does too.
			if k8score.IsBuiltInAPIGroup(gv.Group) && gv.Group != "resource.k8s.io" {
				continue
			}
			canWatch := false
			for _, verb := range r.Verbs {
				if verb == "watch" {
					canWatch = true
				}
			}
			if canWatch {
				s.gvrs = append(s.gvrs, gvr)
			} else {
				s.unsupported = append(s.unsupported, gvr.String()+": discovery has no watch verb")
			}
		}
	}
	s.dynamic = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), kinds)
	typedScheme := runtime.NewScheme()
	if err := scheme.AddToScheme(typedScheme); err != nil {
		return nil, err
	}
	seenTyped := map[string]bool{}
	for _, obj := range objects {
		gvk := obj.GroupVersionKind()
		gvr, ok := byGVK[gvk]
		if !ok {
			return nil, fmt.Errorf("%s %s missing listable discovery entry", gvk, obj.GetName())
		}
		if err := s.dynamic.Tracker().Create(gvr, obj, obj.GetNamespace()); err != nil {
			return nil, err
		}
		typed, err := typedScheme.New(gvk)
		if runtime.IsNotRegisteredError(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, typed); err != nil {
			return nil, fmt.Errorf("decode %s: %w", gvk, err)
		}
		key := strings.Join([]string{gvk.Group, gvk.Kind, obj.GetNamespace(), obj.GetName()}, "/")
		if seenTyped[key] {
			continue
		}
		seenTyped[key] = true
		if err := s.client.Tracker().Add(typed); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func mustLoadReplay(path string) *replayState {
	s, err := loadReplay(path)
	if err != nil {
		log.Fatal(err)
	}
	return s
}

func (s *replayState) mustStart() {
	if err := s.start(); err != nil {
		log.Fatal(err)
	}
}

func (s *replayState) start() error {
	if err := k8s.InitTestReplayClients(s.dynamic, s.discovery); err != nil {
		return err
	}
	cache := k8s.GetDynamicResourceCache()
	for _, gvr := range s.gvrs {
		if err := cache.EnsureWatching(gvr); err != nil {
			s.unsupported = append(s.unsupported, fmt.Sprintf("%s: %v", gvr, err))
			continue
		}
		if !cache.WaitForSync(gvr, 10*time.Second) {
			s.unsupported = append(s.unsupported, gvr.String()+": sync timeout")
		}
	}
	fmt.Fprintf(os.Stderr, "Replay caches synced: %d objects; %d unsupported resources\n", s.objects, len(s.unsupported))
	for _, gap := range s.unsupported {
		fmt.Fprintln(os.Stderr, "Replay unsupported:", gap)
	}
	for _, group := range s.failedGroups {
		fmt.Fprintln(os.Stderr, "Replay capture failed group:", group)
	}
	for _, gap := range s.skipped {
		fmt.Fprintf(os.Stderr, "Replay capture skipped: %s: %s\n", gap.Resource, gap.Error)
	}
	return nil
}

type replayDiscovery struct {
	*discoveryfake.FakeDiscovery
	groups []*metav1.APIGroup
}

func (d *replayDiscovery) ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	return d.groups, d.Resources, nil
}
func (d *replayDiscovery) ServerGroups() (*metav1.APIGroupList, error) {
	list := &metav1.APIGroupList{}
	for _, g := range d.groups {
		list.Groups = append(list.Groups, *g)
	}
	return list, nil
}

func startReplayAdmin(port int, replay *replayState) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/loadtest/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ready": true, "objects": replay.objects, "unsupported": replay.unsupported, "failed_groups": replay.failedGroups, "skipped": replay.skipped})
	})
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Print(err)
		}
	}()
	return srv
}
