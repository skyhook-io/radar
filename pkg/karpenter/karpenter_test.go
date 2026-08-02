package karpenter

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func TestNodeClassRefForNodePool(t *testing.T) {
	tests := []struct {
		name       string
		apiVersion string
		ref        map[string]any
		want       NodeClassRef
		wantOK     bool
	}{
		{
			name:       "v1 group reference",
			apiVersion: APIVersionV1,
			ref:        map[string]any{"group": "karpenter.k8s.aws", "kind": "EC2NodeClass", "name": "default"},
			want:       NodeClassRef{Group: "karpenter.k8s.aws", Kind: "EC2NodeClass", Name: "default"},
			wantOK:     true,
		},
		{
			name:       "v1beta1 apiVersion reference",
			apiVersion: APIVersionV1Beta1,
			ref:        map[string]any{"apiVersion": "karpenter.k8s.aws/v1beta1", "kind": "EC2NodeClass", "name": "default"},
			want: NodeClassRef{
				Group:      "karpenter.k8s.aws",
				Version:    "v1beta1",
				Kind:       "EC2NodeClass",
				Name:       "default",
				APIVersion: "karpenter.k8s.aws/v1beta1",
			},
			wantOK: true,
		},
		{
			name:       "v1 does not fall back to legacy apiVersion",
			apiVersion: APIVersionV1,
			ref:        map[string]any{"apiVersion": "karpenter.k8s.aws/v1beta1", "kind": "EC2NodeClass", "name": "default"},
		},
		{
			name:       "v1beta1 does not fall back to v1 group",
			apiVersion: APIVersionV1Beta1,
			ref:        map[string]any{"group": "karpenter.k8s.aws", "kind": "EC2NodeClass", "name": "default"},
		},
		{
			name:       "v1beta1 rejects version without group",
			apiVersion: APIVersionV1Beta1,
			ref:        map[string]any{"apiVersion": "v1beta1", "kind": "EC2NodeClass", "name": "default"},
		},
		{
			name:       "missing name",
			apiVersion: APIVersionV1,
			ref:        map[string]any{"group": "karpenter.k8s.aws", "kind": "EC2NodeClass"},
		},
		{
			name:       "unknown NodePool API version",
			apiVersion: "karpenter.sh/v2",
			ref:        map[string]any{"group": "karpenter.k8s.aws", "kind": "EC2NodeClass", "name": "default"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := nodePool(tt.apiVersion, "default", "pool-uid", map[string]any{
				"spec": map[string]any{
					"template": map[string]any{
						"spec": map[string]any{"nodeClassRef": tt.ref},
					},
				},
			})
			got, ok := NodeClassRefForNodePool(pool)
			if ok != tt.wantOK {
				t.Fatalf("NodeClassRefForNodePool() ok = %v, want %v", ok, tt.wantOK)
			}
			if got != tt.want {
				t.Fatalf("NodeClassRefForNodePool() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestNodeClassRefForNodeClaim(t *testing.T) {
	claim := object(APIVersionV1, NodeClaimKind, "claim-a", "claim-uid", map[string]any{
		"spec": map[string]any{
			"nodeClassRef": map[string]any{"group": "eks.amazonaws.com", "kind": "NodeClass", "name": "general-purpose"},
		},
	})
	got, ok := NodeClassRefForNodeClaim(claim)
	if !ok {
		t.Fatal("NodeClassRefForNodeClaim() did not find the v1 reference")
	}
	want := NodeClassRef{Group: "eks.amazonaws.com", Kind: "NodeClass", Name: "general-purpose"}
	if got != want {
		t.Fatalf("NodeClassRefForNodeClaim() = %+v, want %+v", got, want)
	}
}

func TestNodePoolConditionsAndReadiness(t *testing.T) {
	transition := "2026-07-13T08:00:00Z"
	pool := nodePool(APIVersionV1, "default", "pool-uid", map[string]any{
		"status": map[string]any{
			"conditions": []any{
				map[string]any{
					"type":               "NodeClassReady",
					"status":             "False",
					"observedGeneration": int64(4),
					"lastTransitionTime": transition,
					"reason":             "NodeClassNotReady",
					"message":            "dependency is not ready",
				},
				map[string]any{
					"type":               "Ready",
					"status":             "False",
					"observedGeneration": int64(4),
					"lastTransitionTime": transition,
					"reason":             "NodeClassNotReady",
					"message":            "NodeClass dependency is not ready",
				},
			},
		},
	})

	conditions := NodePoolConditions(pool)
	if len(conditions) != 2 {
		t.Fatalf("NodePoolConditions() returned %d conditions, want 2", len(conditions))
	}
	ready := NodePoolReadyCondition(pool)
	if ready == nil {
		t.Fatal("NodePoolReadyCondition() returned nil")
	}
	if ready.Reason != "NodeClassNotReady" || ready.Message != "NodeClass dependency is not ready" || ready.ObservedGeneration != 4 {
		t.Fatalf("NodePoolReadyCondition() lost condition fields: %+v", ready)
	}
	wantTime, err := time.Parse(time.RFC3339, transition)
	if err != nil {
		t.Fatal(err)
	}
	if !ready.LastTransitionTime.Time.Equal(wantTime) {
		t.Fatalf("LastTransitionTime = %s, want %s", ready.LastTransitionTime.Time, wantTime)
	}
	if got := NodePoolReadiness(pool); got != ReadinessNotReady {
		t.Fatalf("NodePoolReadiness() = %q, want %q", got, ReadinessNotReady)
	}

	pool.Object["status"].(map[string]any)["conditions"].([]any)[1].(map[string]any)["status"] = "True"
	if got := NodePoolReadiness(pool); got != ReadinessReady {
		t.Fatalf("NodePoolReadiness() = %q, want %q", got, ReadinessReady)
	}
	pool.Object["status"].(map[string]any)["conditions"].([]any)[1].(map[string]any)["status"] = "Unknown"
	if got := NodePoolReadiness(pool); got != ReadinessUnknown {
		t.Fatalf("NodePoolReadiness() = %q, want %q", got, ReadinessUnknown)
	}
	delete(pool.Object, "status")
	if got := NodePoolReadiness(pool); got != ReadinessUnknown {
		t.Fatalf("NodePoolReadiness() without conditions = %q, want %q", got, ReadinessUnknown)
	}
}

func TestNodePoolReadinessDoesNotTrustAStaleGeneration(t *testing.T) {
	pool := nodePool(APIVersionV1, "default", "pool-uid", map[string]any{
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "Ready", "status": "True", "observedGeneration": int64(2)},
			},
		},
	})
	pool.SetGeneration(3)
	if got := NodePoolReadiness(pool); got != ReadinessUnknown {
		t.Fatalf("NodePoolReadiness() = %q, want unknown for a stale Ready condition", got)
	}
}

func TestNodeClaimConditionsAndReadiness(t *testing.T) {
	claim := object(APIVersionV1, NodeClaimKind, "claim-a", "claim-uid", map[string]any{
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "Initialized", "status": "True", "observedGeneration": int64(3)},
				map[string]any{"type": "Ready", "status": "True", "observedGeneration": int64(3), "reason": "Ready"},
				"malformed",
			},
		},
	})
	claim.SetGeneration(3)

	conditions := NodeClaimConditions(claim)
	if len(conditions) != 2 {
		t.Fatalf("NodeClaimConditions() returned %d conditions, want 2", len(conditions))
	}
	ready := NodeClaimReadyCondition(claim)
	if ready == nil || ready.Reason != "Ready" {
		t.Fatalf("NodeClaimReadyCondition() = %+v", ready)
	}
	if got := NodeClaimReadiness(claim); got != ReadinessReady {
		t.Fatalf("NodeClaimReadiness() = %q, want %q", got, ReadinessReady)
	}

	claim.SetGeneration(4)
	if got := NodeClaimReadiness(claim); got != ReadinessUnknown {
		t.Fatalf("NodeClaimReadiness() = %q, want unknown for a stale condition", got)
	}
}

func TestKarpenterStatusResourceLists(t *testing.T) {
	pool := nodePool(APIVersionV1, "default", "pool-uid", map[string]any{
		"status": map[string]any{
			"resources": map[string]any{
				"cpu":            "1250m",
				"memory":         "8Gi",
				"nvidia.com/gpu": int64(2),
				"invalid":        map[string]any{"quantity": "nope"},
			},
		},
	})
	claim := object(APIVersionV1, NodeClaimKind, "claim-a", "claim-uid", map[string]any{
		"status": map[string]any{
			"capacity": map[string]any{
				"cpu":               float64(2.5),
				"memory":            "16Gi",
				"ephemeral-storage": "100Gi",
			},
		},
	})

	poolResources := NodePoolStatusResources(pool)
	gpu := poolResources[corev1.ResourceName("nvidia.com/gpu")]
	if poolResources.Cpu().String() != "1250m" || poolResources.Memory().String() != "8Gi" || gpu.String() != "2" {
		t.Fatalf("NodePoolStatusResources() = %#v", poolResources)
	}
	if _, ok := poolResources[corev1.ResourceName("invalid")]; ok {
		t.Fatal("NodePoolStatusResources() retained an invalid quantity")
	}

	claimCapacity := NodeClaimCapacity(claim)
	ephemeral := claimCapacity[corev1.ResourceEphemeralStorage]
	if claimCapacity.Cpu().String() != "2500m" || claimCapacity.Memory().String() != "16Gi" || ephemeral.String() != "100Gi" {
		t.Fatalf("NodeClaimCapacity() = %#v", claimCapacity)
	}
	if NodePoolStatusResources(nil) != nil || NodeClaimCapacity(nil) != nil {
		t.Fatal("nil resources fabricated status capacity")
	}
}

func TestNormalizeNodePoolSpecV1(t *testing.T) {
	pool := nodePool(APIVersionV1, "default", "pool-uid", map[string]any{
		"spec": map[string]any{
			"replicas": int64(3),
			"weight":   int64(50),
			"limits": map[string]any{
				"cpu":            int64(1000),
				"memory":         "64Gi",
				"nvidia.com/gpu": "8",
				"invalid":        map[string]any{"not": "a quantity"},
			},
			"template": map[string]any{
				"metadata": map[string]any{
					"labels": map[string]any{"team": "platform", "workload": "batch"},
				},
				"spec": map[string]any{
					"nodeClassRef": map[string]any{"group": "karpenter.k8s.aws", "kind": "EC2NodeClass", "name": "default"},
					"requirements": []any{
						map[string]any{"key": "kubernetes.io/arch", "operator": "In", "values": []any{"amd64", "arm64"}, "minValues": int64(2)},
					},
					"taints": []any{
						map[string]any{"key": "dedicated", "value": "batch", "effect": "NoSchedule"},
					},
					"startupTaints": []any{
						map[string]any{"key": "node.cilium.io/agent-not-ready", "value": "true", "effect": "NoExecute"},
					},
					"expireAfter":            "720h",
					"terminationGracePeriod": "24h",
				},
			},
			"disruption": map[string]any{
				"consolidationPolicy": "WhenEmptyOrUnderutilized",
				"consolidateAfter":    "30s",
				"expireAfter":         "legacy-path-must-not-win",
				"budgets": []any{
					map[string]any{"nodes": "20%", "schedule": "0 9 * * mon-fri", "duration": "8h", "reasons": []any{"Drifted", "Underutilized"}},
					map[string]any{"nodes": int64(2)},
				},
			},
		},
	})

	got := NormalizeNodePoolSpec(pool)
	if got.Replicas == nil || *got.Replicas != 3 {
		t.Fatalf("Replicas = %v, want 3", got.Replicas)
	}
	if got.Weight == nil || *got.Weight != 50 {
		t.Fatalf("Weight = %v, want 50", got.Weight)
	}
	gpu := got.Limits[corev1.ResourceName("nvidia.com/gpu")]
	if got.Limits.Cpu().String() != "1k" || got.Limits.Memory().String() != "64Gi" || gpu.String() != "8" {
		t.Fatalf("Limits were not normalized: %#v", got.Limits)
	}
	if _, exists := got.Limits[corev1.ResourceName("invalid")]; exists {
		t.Fatal("invalid quantity was retained")
	}
	if got.TemplateLabels["team"] != "platform" || got.TemplateLabels["workload"] != "batch" {
		t.Fatalf("TemplateLabels = %#v", got.TemplateLabels)
	}
	if got.NodeClassRef == nil || *got.NodeClassRef != (NodeClassRef{Group: "karpenter.k8s.aws", Kind: "EC2NodeClass", Name: "default"}) {
		t.Fatalf("NodeClassRef = %+v", got.NodeClassRef)
	}
	if len(got.Requirements) != 1 || got.Requirements[0].Operator != corev1.NodeSelectorOpIn || got.Requirements[0].MinValues == nil || *got.Requirements[0].MinValues != 2 {
		t.Fatalf("Requirements = %+v", got.Requirements)
	}
	if len(got.Taints) != 1 || got.Taints[0] != (corev1.Taint{Key: "dedicated", Value: "batch", Effect: corev1.TaintEffectNoSchedule}) {
		t.Fatalf("Taints = %+v", got.Taints)
	}
	if len(got.StartupTaints) != 1 || got.StartupTaints[0].Effect != corev1.TaintEffectNoExecute {
		t.Fatalf("StartupTaints = %+v", got.StartupTaints)
	}
	if got.ExpireAfter != "720h" || got.TerminationGracePeriod != "24h" {
		t.Fatalf("lifecycle fields = expire %q, termination grace %q", got.ExpireAfter, got.TerminationGracePeriod)
	}
	if got.Disruption.ConsolidationPolicy != "WhenEmptyOrUnderutilized" || got.Disruption.ConsolidateAfter != "30s" {
		t.Fatalf("Disruption = %+v", got.Disruption)
	}
	if len(got.Disruption.Budgets) != 2 || len(got.Disruption.Budgets[0].Reasons) != 2 || got.Disruption.Budgets[0].Nodes != "20%" || got.Disruption.Budgets[1].Nodes != "2" {
		t.Fatalf("Budgets = %+v", got.Disruption.Budgets)
	}
}

func TestNormalizeNodePoolSpecV1Beta1UsesDocumentedPaths(t *testing.T) {
	pool := nodePool(APIVersionV1Beta1, "default", "pool-uid", map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"nodeClassRef": map[string]any{"apiVersion": "karpenter.k8s.aws/v1beta1", "kind": "EC2NodeClass", "name": "default"},
					"expireAfter":  "v1-path-must-not-win",
				},
			},
			"disruption": map[string]any{"expireAfter": "720h"},
		},
	})

	got := NormalizeNodePoolSpec(pool)
	if got.ExpireAfter != "720h" {
		t.Fatalf("ExpireAfter = %q, want documented v1beta1 disruption path", got.ExpireAfter)
	}
	if got.NodeClassRef == nil || got.NodeClassRef.Group != "karpenter.k8s.aws" || got.NodeClassRef.Version != "v1beta1" {
		t.Fatalf("NodeClassRef = %+v", got.NodeClassRef)
	}
}

func TestResolveNodePoolForClaim(t *testing.T) {
	defaultPool := nodePool(APIVersionV1, "default", "pool-uid", nil)
	otherPool := nodePool(APIVersionV1, "other", "other-uid", nil)
	pools := map[string]*unstructured.Unstructured{"default": defaultPool, "other": otherPool}

	t.Run("matching owner reference wins", func(t *testing.T) {
		claim := claimWith("claim-a", map[string]string{NodePoolLabelKey: "other"}, metav1.OwnerReference{
			APIVersion: APIVersionV1,
			Kind:       NodePoolKind,
			Name:       "default",
			UID:        types.UID("pool-uid"),
		})
		got, source := ResolveNodePoolForClaim(claim, pools)
		if got != defaultPool || source != RelationshipOwnerRef {
			t.Fatalf("ResolveNodePoolForClaim() = %v, %q; want default, owner_ref", objectName(got), source)
		}
	})

	t.Run("UID mismatch blocks label fallback", func(t *testing.T) {
		claim := claimWith("claim-a", map[string]string{NodePoolLabelKey: "default"}, metav1.OwnerReference{
			APIVersion: APIVersionV1,
			Kind:       NodePoolKind,
			Name:       "default",
			UID:        types.UID("deleted-pool-uid"),
		})
		got, source := ResolveNodePoolForClaim(claim, pools)
		if got != nil || source != RelationshipNone {
			t.Fatalf("ResolveNodePoolForClaim() = %v, %q; want no stale relationship", objectName(got), source)
		}
	})

	t.Run("missing owner target blocks label fallback", func(t *testing.T) {
		claim := claimWith("claim-a", map[string]string{NodePoolLabelKey: "default"}, metav1.OwnerReference{
			APIVersion: APIVersionV1,
			Kind:       NodePoolKind,
			Name:       "deleted",
			UID:        types.UID("deleted-pool-uid"),
		})
		got, source := ResolveNodePoolForClaim(claim, pools)
		if got != nil || source != RelationshipNone {
			t.Fatalf("ResolveNodePoolForClaim() = %v, %q; want no relationship", objectName(got), source)
		}
	})

	t.Run("label is used only without a Karpenter owner", func(t *testing.T) {
		claim := claimWith("claim-a", map[string]string{NodePoolLabelKey: "default"})
		got, source := ResolveNodePoolForClaim(claim, pools)
		if got != defaultPool || source != RelationshipLabel {
			t.Fatalf("ResolveNodePoolForClaim() = %v, %q; want default, label", objectName(got), source)
		}
	})

	t.Run("same kind in another API group is not authoritative", func(t *testing.T) {
		claim := claimWith("claim-a", map[string]string{NodePoolLabelKey: "default"}, metav1.OwnerReference{
			APIVersion: "example.com/v1",
			Kind:       NodePoolKind,
			Name:       "other",
			UID:        types.UID("other-uid"),
		})
		got, source := ResolveNodePoolForClaim(claim, pools)
		if got != defaultPool || source != RelationshipLabel {
			t.Fatalf("ResolveNodePoolForClaim() = %v, %q; want default, label", objectName(got), source)
		}
	})
}

func TestClaimAccessors(t *testing.T) {
	claim := object(APIVersionV1, NodeClaimKind, "claim-a", "claim-uid", map[string]any{
		"status": map[string]any{
			"nodeName":   "ip-10-0-0-1.ec2.internal",
			"providerID": "aws:///us-east-1a/i-0123456789",
		},
	})
	if got := ClaimNodeName(claim); got != "ip-10-0-0-1.ec2.internal" {
		t.Fatalf("ClaimNodeName() = %q", got)
	}
	if got := ClaimProviderID(claim); got != "aws:///us-east-1a/i-0123456789" {
		t.Fatalf("ClaimProviderID() = %q", got)
	}
	delete(claim.Object, "status")
	if ClaimNodeName(claim) != "" || ClaimProviderID(claim) != "" {
		t.Fatal("claim accessors fabricated values for missing status")
	}
}

func nodePool(apiVersion, name, uid string, fields map[string]any) *unstructured.Unstructured {
	return object(apiVersion, NodePoolKind, name, uid, fields)
}

func claimWith(name string, labels map[string]string, owners ...metav1.OwnerReference) *unstructured.Unstructured {
	claim := object(APIVersionV1, NodeClaimKind, name, "claim-uid", nil)
	claim.SetLabels(labels)
	claim.SetOwnerReferences(owners)
	return claim
}

func object(apiVersion, kind, name, uid string, fields map[string]any) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{}}
	for key, value := range fields {
		obj.Object[key] = value
	}
	obj.SetAPIVersion(apiVersion)
	obj.SetKind(kind)
	obj.SetName(name)
	obj.SetUID(types.UID(uid))
	return obj
}

func objectName(obj *unstructured.Unstructured) string {
	if obj == nil {
		return "<nil>"
	}
	return obj.GetName()
}

// TestNodeClaimFailureReasonVocabulary pins the catalogue every capacity
// surface reads: Karpenter keeps a FAILING lifecycle stage at
// status=Unknown with a provider reason, so a reason that falls out of this
// table silently reclassifies a dead NodeClaim as "still provisioning".
func TestNodeClaimFailureReasonVocabulary(t *testing.T) {
	// Every reason the AWS provider's ToReasonMessage can return, plus the
	// Karpenter core reasons. Reasons carrying "fail"/"error" text are listed
	// too — they must stay classified even if the substring rule changes.
	documented := []string{
		// karpenter core
		"LaunchFailed", "CreateError", "InsufficientCapacityError", "NodeClassNotReady",
		"MultipleNodesFound", "UnregisteredTaintMissing", "InsufficientInstanceCapacity",
		// aws provider (pkg/errors ToReasonMessage)
		"SpotSLRCreationFailed", "Unauthorized", "AMIAuthorizationFailure",
		"SecurityGroupSubnetVPCMismatch", "InstanceProfileNameInvalid",
		"LaunchTemplateNotFound", "InvalidAMIID", "AMINotFound", "RequestLimitExceeded",
		"InternalError", "FreeTierIneligible", "FleetQuotaExceeded",
		"AccountPendingVerification", "SpotQuotaExceeded", "VCPULimitExceeded",
		"InsufficientFreeAddressesInSubnet",
	}
	reasons := append([]string{}, documented...)
	for reason := range nodeClaimFailureReasons {
		reasons = append(reasons, reason)
	}

	for _, reason := range reasons {
		t.Run(reason, func(t *testing.T) {
			if !IsFailureReason(reason) {
				t.Fatalf("IsFailureReason(%q) = false — a failing stage would read as in progress", reason)
			}
			condition := metav1.Condition{Type: "Launched", Status: metav1.ConditionUnknown, Reason: reason}
			if !IsFailedLifecycleCondition(condition) {
				t.Fatalf("IsFailedLifecycleCondition(Unknown/%q) = false", reason)
			}
			if !IsFailedLifecycleConditionForVersion(APIVersionV1Beta1, condition) {
				t.Fatalf("IsFailedLifecycleConditionForVersion(v1beta1, Unknown/%q) = false", reason)
			}
		})
	}

	// The complement: ordinary in-progress reasons must NOT be classified as
	// failures, or every provisioning claim would render as broken.
	for _, reason := range []string{"NodeNotFound", "StartupTaintsExist", "Launched", "Registered", ""} {
		if IsFailureReason(reason) {
			t.Fatalf("IsFailureReason(%q) = true — normal in-progress states must not read as failures", reason)
		}
	}
}
