package issues

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// MetalLBGroup is MetalLB's API group.
const MetalLBGroup = "metallb.io"

// ReasonMetalLBConfigurationInvalid marks configuration a MetalLB component
// refused to load.
const ReasonMetalLBConfigurationInvalid = "MetalLBConfigurationInvalid"

const (
	metalLBComponentLabel = "metallb.io/component-type"
	metalLBNodeLabel      = "metallb.io/node-name"
)

// MetalLBConfigurationIssues reports configuration MetalLB rejected, read from
// the ConfigurationState each component (the controller, and one speaker per
// node) writes in MetalLB's namespace. A component reads Invalid only when a
// reconciler failed to parse the configuration; transient failures leave it
// Valid. The rejected configuration is not applied: the component keeps the
// last one it accepted.
//
// Every speaker parses the same objects, so one bad object makes them all
// Invalid with the same errors. Reporting each state would file one finding
// per node for one mistake, so states are grouped by namespace and error
// summary: each distinct rejection is one warning, on the controller's state
// when it shares the errors, otherwise on the first speaker's by name, and
// the message counts the others. Speakers that alone reject a node-scoped
// object (a BGPPeer or BGPAdvertisement whose node selector matches only
// them) report different errors and get their own finding.
//
// The reconcilers rewrite the condition time on every reconcile, so it is not
// the onset and the onset stays unknown.
func MetalLBConfigurationIssues(gvr schema.GroupVersionResource, items []*unstructured.Unstructured, ownedSubjects map[string]bool) []Issue {
	type rejection struct {
		summary string
		states  []*unstructured.Unstructured
	}
	groups := map[string]*rejection{}
	var order []string
	for _, u := range items {
		if u == nil || u.GetKind() != "ConfigurationState" || u.GetDeletionTimestamp() != nil {
			continue
		}
		if ownedSubjects[resourceKey(gvr.Group, "ConfigurationState", u.GetNamespace(), u.GetName())] {
			continue
		}
		if result, _, _ := unstructured.NestedString(u.Object, "status", "result"); result != "Invalid" {
			continue
		}
		summary, _, _ := unstructured.NestedString(u.Object, "status", "errorSummary")
		summary = strings.TrimSpace(summary)
		key := u.GetNamespace() + "\x00" + summary
		g := groups[key]
		if g == nil {
			g = &rejection{summary: summary}
			groups[key] = g
			order = append(order, key)
		}
		g.states = append(g.states, u)
	}
	sort.Strings(order)
	var out []Issue
	for _, key := range order {
		g := groups[key]
		sort.SliceStable(g.states, func(i, j int) bool {
			ci, cj := isMetalLBController(g.states[i]), isMetalLBController(g.states[j])
			if ci != cj {
				return ci
			}
			return g.states[i].GetName() < g.states[j].GetName()
		})
		anchor := g.states[0]
		message := describeMetalLBComponent(anchor) + " rejected the MetalLB configuration and keeps running the last configuration it accepted, if any"
		if errs := metalLBErrorLines(g.summary); len(errs) > 0 {
			message += ": " + strings.Join(errs, "; ")
		}
		if others := metalLBOtherComponents(g.states[1:]); others != "" {
			message += ". Also rejected by " + others
		}
		out = append(out, newConditionIssue(gvr, "ConfigurationState", anchor.GetNamespace(), anchor.GetName(), SeverityWarning, ReasonMetalLBConfigurationInvalid, message, time.Time{}, false, "", anchor.GetCreationTimestamp().Time))
	}
	out, _ = filterShapedIssues(out, Filters{})
	return out
}

func isMetalLBController(u *unstructured.Unstructured) bool {
	return u.GetLabels()[metalLBComponentLabel] == "controller"
}

func describeMetalLBComponent(u *unstructured.Unstructured) string {
	labels := u.GetLabels()
	switch labels[metalLBComponentLabel] {
	case "controller":
		return "The MetalLB controller"
	case "speaker":
		if node := labels[metalLBNodeLabel]; node != "" {
			return "The MetalLB speaker on node " + node
		}
		return "A MetalLB speaker"
	}
	return "MetalLB component " + u.GetName()
}

func metalLBOtherComponents(states []*unstructured.Unstructured) string {
	if len(states) == 0 {
		return ""
	}
	controller, speakers, other := 0, 0, 0
	for _, u := range states {
		switch u.GetLabels()[metalLBComponentLabel] {
		case "controller":
			controller++
		case "speaker":
			speakers++
		default:
			other++
		}
	}
	var parts []string
	if controller > 0 {
		parts = append(parts, "the controller")
	}
	if speakers == 1 {
		parts = append(parts, "1 other speaker")
	} else if speakers > 1 {
		parts = append(parts, fmt.Sprintf("%d other speakers", speakers))
	}
	if other == 1 {
		parts = append(parts, "1 other component")
	} else if other > 1 {
		parts = append(parts, fmt.Sprintf("%d other components", other))
	}
	return strings.Join(parts, " and ")
}

// metalLBErrorLines splits an errorSummary, which joins one message per
// failing reconciler with newlines.
func metalLBErrorLines(summary string) []string {
	var out []string
	for _, line := range strings.Split(summary, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}
