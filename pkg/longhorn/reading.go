package longhorn

import (
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Why a Volume's healthy replica count is unknown.
const (
	ReplicasEngineUnread    = "engineUnread"    // the caller can't read Engines in the Volume's namespace
	ReplicasEngineAmbiguous = "engineAmbiguous" // several Engines and none or several marked spec.active
	ReplicasNoEngine        = "noEngine"        // Engines were read and none names the Volume
	ReplicasNoModes         = "noModes"         // the Engine reports no replica modes, as when it is stopped
)

// UseState says how certain a Volume's current PV is.
type UseState string

const (
	UseBound   UseState = "bound"   // a live PV names the Volume
	UseNone    UseState = "none"    // every PV was read and none names it
	UseUnknown UseState = "unknown" // PVs unread, or the Volume's name is not unique among those read
)

type ClaimRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// VolumeUse is the PV a Volume backs now, proven by the live PV's spec.csi,
// never by the names Longhorn last recorded in status.kubernetesStatus.
type VolumeUse struct {
	State            UseState  `json:"state"`
	PersistentVolume string    `json:"persistentVolume,omitempty"`
	Claim            *ClaimRef `json:"claim,omitempty"`
	// ClaimWithheld: the PV is bound to a claim in a namespace the caller
	// can't read, so it is not named.
	ClaimWithheld bool `json:"claimWithheld,omitempty"`
}

// VolumeReading is one Volume as the workspace shows it. A field Longhorn
// didn't write is absent, never zero.
type VolumeReading struct {
	Namespace        string `json:"namespace"`
	Name             string `json:"name"`
	UID              string `json:"uid"`
	Health           Health `json:"health"`
	State            string `json:"state,omitempty"`
	Robustness       string `json:"robustness,omitempty"`
	Size             *int64 `json:"size,omitempty"`
	ActualSize       *int64 `json:"actualSize,omitempty"`
	NumberOfReplicas *int64 `json:"numberOfReplicas,omitempty"`
	// HealthyReplicas counts the Engine's replicas in mode RW; null when
	// unknown (HealthyReplicasUnknown says why), never the requested count.
	HealthyReplicas        *int64    `json:"healthyReplicas"`
	HealthyReplicasUnknown string    `json:"healthyReplicasUnknown,omitempty"`
	LastDegradedAt         string    `json:"lastDegradedAt,omitempty"`
	Use                    VolumeUse `json:"use"`
}

// HealthCounts counts the Volumes read by Health. Complete is false when
// some Volumes could not be read: each count is then a lower bound.
type HealthCounts struct {
	Healthy  int  `json:"healthy"`
	Degraded int  `json:"degraded"`
	Faulted  int  `json:"faulted"`
	Detached int  `json:"detached"`
	Unknown  int  `json:"unknown"`
	Complete bool `json:"complete"`
}

func (c *HealthCounts) Add(h Health) {
	switch h {
	case HealthHealthy:
		c.Healthy++
	case HealthDegraded:
		c.Degraded++
	case HealthFaulted:
		c.Faulted++
	case HealthDetached:
		c.Detached++
	default:
		c.Unknown++
	}
}

// ReadVolume reads v with its namespace's Engines (enginesRead: the caller
// could read them there). Use is left for the caller, which holds the PVs.
func ReadVolume(v *unstructured.Unstructured, engines []*unstructured.Unstructured, enginesRead bool) VolumeReading {
	str := func(f ...string) string { s, _, _ := unstructured.NestedString(v.Object, f...); return s }
	r := VolumeReading{
		Namespace: v.GetNamespace(), Name: v.GetName(), UID: string(v.GetUID()),
		Health: VolumeHealth(v), State: str("status", "state"), Robustness: str("status", "robustness"),
		LastDegradedAt: str("status", "lastDegradedAt"), Use: VolumeUse{State: UseUnknown},
	}
	// spec.size is a decimal string of bytes; status.actualSize an integer.
	if n, err := strconv.ParseInt(str("spec", "size"), 10, 64); err == nil {
		r.Size = &n
	}
	if n, found, err := unstructured.NestedInt64(v.Object, "status", "actualSize"); found && err == nil {
		r.ActualSize = &n
	}
	if n, found, err := unstructured.NestedInt64(v.Object, "spec", "numberOfReplicas"); found && err == nil {
		r.NumberOfReplicas = &n
	}
	r.HealthyReplicas, r.HealthyReplicasUnknown = healthyReplicas(v.GetName(), engines, enginesRead)
	return r
}

// healthyReplicas follows the Volume drawer: the Volume's one Engine, or
// while it migrates the one marked spec.active.
func healthyReplicas(volume string, engines []*unstructured.Unstructured, read bool) (*int64, string) {
	if !read {
		return nil, ReplicasEngineUnread
	}
	var candidates, active []*unstructured.Unstructured
	for _, e := range engines {
		if name, _, _ := unstructured.NestedString(e.Object, "spec", "volumeName"); name != volume {
			continue
		}
		candidates = append(candidates, e)
		if on, _, _ := unstructured.NestedBool(e.Object, "spec", "active"); on {
			active = append(active, e)
		}
	}
	var engine *unstructured.Unstructured
	switch {
	case len(candidates) == 0:
		return nil, ReplicasNoEngine
	case len(candidates) == 1:
		engine = candidates[0]
	case len(active) == 1:
		engine = active[0]
	default:
		return nil, ReplicasEngineAmbiguous
	}
	modes, found, err := unstructured.NestedStringMap(engine.Object, "status", "replicaModeMap")
	if !found || err != nil {
		return nil, ReplicasNoModes
	}
	var n int64
	for _, mode := range modes {
		if mode == "RW" {
			n++
		}
	}
	return &n, ""
}

// PVHandle is the Longhorn Volume a PV is backed by, or "".
func PVHandle(pv *corev1.PersistentVolume) string {
	if pv == nil || pv.Spec.CSI == nil || pv.Spec.CSI.Driver != CSIDriver {
		return ""
	}
	return pv.Spec.CSI.VolumeHandle
}

// UseOf is the Volume's current PV among pvs (all of the cluster's, read).
// A claim is named only when the PV is Bound to it and claimReadable allows
// its namespace.
func UseOf(volume string, pvs []*corev1.PersistentVolume, claimReadable func(namespace string) bool) VolumeUse {
	var match []*corev1.PersistentVolume
	for _, pv := range pvs {
		if PVHandle(pv) == volume {
			match = append(match, pv)
		}
	}
	switch len(match) {
	case 0:
		return VolumeUse{State: UseNone}
	case 1:
	default:
		return VolumeUse{State: UseUnknown}
	}
	pv := match[0]
	use := VolumeUse{State: UseBound, PersistentVolume: pv.Name}
	if ref := pv.Spec.ClaimRef; ref != nil && pv.Status.Phase == corev1.VolumeBound {
		if claimReadable(ref.Namespace) {
			use.Claim = &ClaimRef{Namespace: ref.Namespace, Name: ref.Name}
		} else {
			use.ClaimWithheld = true
		}
	}
	return use
}
