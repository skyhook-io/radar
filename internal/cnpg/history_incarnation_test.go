package cnpg

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestFleetMetricsExcludePredecessorLookbackWindows(t *testing.T) {
	lag := 30.0
	growth := 1024.0
	cases := []struct {
		name        string
		age, pvcAge time.Duration
		lagState    string
		sustained   bool
		growthState string
	}{
		{"new cluster", time.Minute, 0, usageStateNotRead, false, usageStateNotRead},
		{"current gauges but incomplete lag window", 7 * time.Minute, 0, historyStateOK, false, usageStateNotRead},
		{"offset selector still reaches predecessor lookback", 12 * time.Minute, 0, historyStateOK, false, usageStateNotRead},
		{"current lag window but incomplete growth", time.Hour, 0, historyStateOK, true, usageStateNotRead},
		{"all current", 24 * time.Hour, 24 * time.Hour, historyStateOK, true, historyStateOK},
		{"recreated PVC", 24 * time.Hour, time.Hour, historyStateOK, true, usageStateNotRead},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "main"}}}
			c.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-tc.age)))
			claims := map[string][]*corev1.PersistentVolumeClaim{}
			if tc.pvcAge > 0 {
				claims["main"] = []*corev1.PersistentVolumeClaim{{ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(time.Now().Add(-tc.pvcAge))}}}
			}
			rows := cnpgFleetMetricsRows("pg", []*unstructured.Unstructured{c}, []CNPGFleetLag{{State: historyStateOK, Seconds: &lag, SustainedSeconds: &lag, ReceiverDownSustained: []string{"main-2"}}}, []CNPGFleetSlots{{State: historyStateOK}}, []CNPGFleetGrowth{{State: historyStateOK, BytesPerHour: &growth}}, claims)
			row := rows[0]
			if row.Lag.State != tc.lagState || (row.Lag.SustainedSeconds != nil) != tc.sustained || row.Growth.State != tc.growthState {
				t.Fatalf("metrics = %+v", row)
			}
			if tc.age < 5*time.Minute && (row.Slots.State != usageStateNotRead || len(row.Lag.ReceiverDownSustained) != 0) {
				t.Fatalf("predecessor gauges survived: %+v", row)
			}
		})
	}
}

func TestFleetMetricsRetainDeniedAndFailedReads(t *testing.T) {
	c := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "main"}}}
	c.SetCreationTimestamp(metav1.NewTime(time.Now()))
	grant := grantGetPods.In("pg").Ref()
	row := cnpgFleetMetricsRows("pg", []*unstructured.Unstructured{c}, []CNPGFleetLag{{State: cnpgHistoryStateDenied, Grant: grant}}, []CNPGFleetSlots{{State: historyStateError, Reason: "query failed"}}, []CNPGFleetGrowth{{State: historyStateError, Reason: "query failed"}}, nil)[0]
	if row.Lag.State != cnpgHistoryStateDenied || row.Lag.Grant != grant || row.Slots.Reason != "query failed" || row.Growth.Reason != "query failed" {
		t.Fatalf("coverage was replaced by waiting: %+v", row)
	}
}
