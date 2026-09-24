package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync/atomic"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/settings"
)

const usageRecordKey = "usage.json"

// usageRecord is the part of settings the usage-data collector owns.
type usageRecord struct {
	UsageData          *settings.UsageDataChoice `json:"usageData,omitempty"`
	UsagePromptShownAt *time.Time                `json:"usagePromptShownAt,omitempty"`
}

// configMapUsageStore keeps a shared Radar's usage-data choice in the
// ConfigMap the chart creates, so a pod restart doesn't forget what the team
// decided. When the ConfigMap or its Role is missing (an older chart, or
// rbac.create=false without matching RBAC) it falls back to settings.json for
// the rest of the process, so the choice is per pod.
type configMapUsageStore struct {
	namespace, name string
	client          func() kubernetes.Interface
	fallback        atomic.Bool
	checked         atomic.Bool
}

func newConfigMapUsageStore() *configMapUsageStore {
	name := os.Getenv("RADAR_USAGE_CONFIGMAP")
	namespace := os.Getenv("MY_POD_NAMESPACE")
	if name == "" || namespace == "" {
		return nil
	}
	return &configMapUsageStore{
		namespace: namespace,
		name:      name,
		client: func() kubernetes.Interface {
			if c := k8s.GetClient(); c != nil {
				return c
			}
			return nil
		},
	}
}

// Scope reports where the choice currently lives, for Settings.
func (st *configMapUsageStore) Scope() string {
	if st.fallback.Load() {
		return "pod"
	}
	return "cluster"
}

func (st *configMapUsageStore) useFallback(err error) bool {
	if st.fallback.Load() {
		return true
	}
	if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
		if st.fallback.CompareAndSwap(false, true) {
			log.Printf("[usage] Usage-data ConfigMap %s/%s unavailable (%v); keeping the choice in this pod", st.namespace, st.name, err)
		}
		return true
	}
	return false
}

// checkWritable decides once whether Radar may update the ConfigMap. Reading
// is not enough: the main ClusterRole reads every ConfigMap, so without the
// write grant an opt-out would land in the pod while the ConfigMap kept the
// old "on" for the next pod to find. Without write access the ConfigMap is
// ignored from the start, and the worst a restart can do is ask again. An
// error means Radar can't tell yet, and the caller must not trust the
// ConfigMap until it can.
func (st *configMapUsageStore) checkWritable(client kubernetes.Interface) error {
	if st.checked.Load() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	review, err := client.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{
		Spec: authorizationv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Namespace: st.namespace, Verb: "update", Resource: "configmaps", Name: st.name,
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("check write access to ConfigMap %s/%s: %w", st.namespace, st.name, err)
	}
	st.checked.Store(true)
	if !review.Status.Allowed && st.fallback.CompareAndSwap(false, true) {
		log.Printf("[usage] No write access to ConfigMap %s/%s; keeping the usage-data choice in this pod", st.namespace, st.name)
	}
	return nil
}

func (st *configMapUsageStore) Load() (settings.Settings, error) {
	if st.fallback.Load() {
		return settings.LoadChecked()
	}
	client := st.client()
	if client == nil {
		return settings.Settings{}, fmt.Errorf("kubernetes client not ready")
	}
	if err := st.checkWritable(client); err != nil {
		return settings.Settings{}, err
	}
	if st.fallback.Load() {
		return settings.LoadChecked()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cm, err := client.CoreV1().ConfigMaps(st.namespace).Get(ctx, st.name, metav1.GetOptions{})
	if err != nil {
		if st.useFallback(err) {
			return settings.LoadChecked()
		}
		return settings.Settings{}, err
	}
	rec, err := decodeUsageRecord(cm.Data[usageRecordKey])
	if err != nil {
		return settings.Settings{}, err
	}
	return settings.Settings{UsageData: rec.UsageData, UsagePromptShownAt: rec.UsagePromptShownAt}, nil
}

func (st *configMapUsageStore) Save(fn func(*settings.Settings)) error {
	if st.fallback.Load() {
		_, err := settings.UpdateChecked(fn)
		return err
	}
	client := st.client()
	if client == nil {
		return fmt.Errorf("kubernetes client not ready")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cm, err := client.CoreV1().ConfigMaps(st.namespace).Get(ctx, st.name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		rec, err := decodeUsageRecord(cm.Data[usageRecordKey])
		if err != nil {
			return err
		}
		s := settings.Settings{UsageData: rec.UsageData, UsagePromptShownAt: rec.UsagePromptShownAt}
		fn(&s)
		data, err := json.Marshal(usageRecord{UsageData: s.UsageData, UsagePromptShownAt: s.UsagePromptShownAt})
		if err != nil {
			return err
		}
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data[usageRecordKey] = string(data)
		_, err = client.CoreV1().ConfigMaps(st.namespace).Update(ctx, cm, metav1.UpdateOptions{})
		return err
	})
	if err != nil && st.useFallback(err) {
		_, err = settings.UpdateChecked(fn)
	}
	return err
}

func decodeUsageRecord(raw string) (usageRecord, error) {
	var rec usageRecord
	if raw == "" {
		return rec, nil
	}
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return usageRecord{}, fmt.Errorf("decode usage-data record: %w", err)
	}
	return rec, nil
}
