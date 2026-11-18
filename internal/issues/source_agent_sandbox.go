package issues

import (
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/agentsandbox"
	"github.com/skyhook-io/radar/pkg/timeutil"
)

// detectAgentSandboxNotReported reports a Sandbox or SandboxClaim the
// controller has written no Ready condition on since it was created, past
// agentsandbox.IncidentAfter. The generic condition walk has no condition to
// raise, so without this it would never be an Issue.
func detectAgentSandboxNotReported(gvr schema.GroupVersionResource, kind string, u *unstructured.Unstructured, now time.Time) []Issue {
	if kind != "Sandbox" && kind != "SandboxClaim" {
		return nil
	}
	if !agentsandbox.UnreportedStalled(u, now) {
		return nil
	}
	created := u.GetCreationTimestamp().Time
	message := fmt.Sprintf("The agent-sandbox controller hasn't reported on this %s in the %s since it was created. Check that the controller is running and reconciles this namespace.", kind, timeutil.FormatAgeShort(now.Sub(created)))
	return []Issue{newConditionIssue(gvr, kind, u.GetNamespace(), u.GetName(), SeverityWarning, agentsandbox.ReasonNotReported, message, created, true, "", created)}
}

// detectWarmPoolTemplateMissing reports each SandboxWarmPool whose
// SandboxTemplate is not in its namespace, judged only where Radar holds
// that namespace's templates in full. The pool carries no condition that says
// so; its claims say TemplateNotFound once one is made.
func detectWarmPoolTemplateMissing(p Provider, poolGVR schema.GroupVersionResource, pools []*unstructured.Unstructured) []Issue {
	if len(pools) == 0 {
		return nil
	}
	var templatesGVR schema.GroupVersionResource
	found := false
	for _, g := range p.WatchedDynamic() {
		if g.Group == agentSandboxExtensionsGroup && p.KindForGVR(g) == "SandboxTemplate" {
			templatesGVR, found = g, true
			break
		}
	}
	if !found {
		return nil
	}
	type read struct {
		names map[string]bool
		ok    bool
	}
	byNS := map[string]read{}
	templatesIn := func(ns string) read {
		if r, ok := byNS[ns]; ok {
			return r
		}
		// Synced is read before the list: the list may start the informer,
		// and only one already synced makes an empty answer mean none.
		synced := true
		if sp, ok := p.(dynamicSyncProvider); ok {
			synced = sp.DynamicNamespaceSynced(templatesGVR, ns)
		}
		items, err := p.ListDynamic(templatesGVR, ns)
		r := read{names: map[string]bool{}, ok: err == nil && synced}
		for _, t := range items {
			r.names[t.GetName()] = true
		}
		byNS[ns] = r
		return r
	}
	var out []Issue
	for _, pool := range pools {
		name := agentsandbox.WarmPoolTemplate(pool)
		if name == "" || pool.GetDeletionTimestamp() != nil {
			continue
		}
		ns := pool.GetNamespace()
		templates := templatesIn(ns)
		if !templates.ok || templates.names[name] {
			continue
		}
		message := fmt.Sprintf("The pool names SandboxTemplate %s, which doesn't exist in %s, so it can't create sandboxes and its claims can't bind.", name, ns)
		iss := newConditionIssue(poolGVR, "SandboxWarmPool", ns, pool.GetName(), SeverityWarning, agentsandbox.ReasonTemplateNotFound, message, time.Time{}, false, "", pool.GetCreationTimestamp().Time)
		iss.RequiredReads = []EvidenceRead{
			{Group: agentSandboxExtensionsGroup, Resource: poolGVR.Resource, Namespace: ns, Verb: "list"},
			{Group: agentSandboxExtensionsGroup, Resource: templatesGVR.Resource, Namespace: ns, Verb: "list"},
		}
		out = append(out, iss)
	}
	return out
}
