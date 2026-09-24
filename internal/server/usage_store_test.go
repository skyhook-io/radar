package server

import (
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/settings"
)

func storeWith(client kubernetes.Interface) *configMapUsageStore {
	return &configMapUsageStore{namespace: "radar", name: "radar-usage-data", client: func() kubernetes.Interface { return client }}
}

// allowWrites makes the fake API server answer access reviews.
func allowWrites(client *fake.Clientset, allowed bool) {
	client.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, &authorizationv1.SelfSubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: allowed}}, nil
	})
}

func TestConfigMapUsageStoreSurvivesRestart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "radar-usage-data", Namespace: "radar"},
	})
	allowWrites(client, true)
	st := storeWith(client)
	s, err := st.Load()
	if err != nil || s.UsageData != nil {
		t.Fatalf("empty ConfigMap: %+v, %v", s, err)
	}
	now := time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)
	if err := st.Save(func(s *settings.Settings) {
		s.UsageData = &settings.UsageDataChoice{Enabled: true, DecidedAt: now, DecidedBy: "dana@example.com"}
		s.UsagePromptShownAt = &now
		s.Theme = "dark" // not ours: must not be written
	}); err != nil {
		t.Fatal(err)
	}

	// A new pod: fresh store, same ConfigMap.
	again, err := storeWith(client).Load()
	if err != nil {
		t.Fatal(err)
	}
	if again.UsageData == nil || !again.UsageData.Enabled || again.UsageData.DecidedBy != "dana@example.com" ||
		again.UsagePromptShownAt == nil {
		t.Fatalf("choice lost across restart: %+v", again)
	}
	if again.Theme != "" {
		t.Fatalf("store wrote a field it does not own")
	}
	if _, err := settings.LoadChecked(); err != nil {
		t.Fatal(err)
	}
	if local := settings.Load(); local.UsageData != nil {
		t.Fatalf("ConfigMap store also wrote settings.json")
	}
}

func TestConfigMapUsageStoreFallsBackWhenMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	missing := fake.NewSimpleClientset()
	allowWrites(missing, true)
	st := storeWith(missing)
	if _, err := st.Load(); err != nil {
		t.Fatalf("missing ConfigMap should fall back, got %v", err)
	}
	if !st.fallback.Load() || st.Scope() != "pod" {
		t.Fatalf("fallback not latched or not reported as pod-scoped: %q", st.Scope())
	}
	if err := st.Save(func(s *settings.Settings) {
		s.UsageData = &settings.UsageDataChoice{Enabled: true}
	}); err != nil {
		t.Fatal(err)
	}
	if local := settings.Load(); local.UsageData == nil || !local.UsageData.Enabled {
		t.Fatalf("fallback did not use settings.json: %+v", local)
	}
}

func TestConfigMapUsageStoreIgnoresReadOnlyConfigMap(t *testing.T) {
	// Readable but not writable: an old "on" in the ConfigMap must not be
	// honored, or an opt-out saved in the pod would come undone on restart.
	t.Setenv("HOME", t.TempDir())
	client := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "radar-usage-data", Namespace: "radar"},
		Data:       map[string]string{usageRecordKey: `{"usageData":{"enabled":true}}`},
	})
	allowWrites(client, false)
	st := storeWith(client)
	s, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if s.UsageData != nil || st.Scope() != "pod" {
		t.Fatalf("read-only ConfigMap was trusted: %+v scope=%s", s.UsageData, st.Scope())
	}
}
