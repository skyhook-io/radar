package topology

import (
	"fmt"
	"log"
	"strings"

	"github.com/skyhook-io/radar/pkg/netpol"
	"github.com/skyhook-io/radar/pkg/resourceid"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	calicoProjectGroup = "projectcalico.org"
	calicoCRDGroup     = "crd.projectcalico.org"
)

type calicoPolicyDefinition struct {
	kind       string
	nodeKind   NodeKind
	resource   string
	namespaced bool
	staged     bool
	kubernetes bool
	// stages names the enforced kind a staged policy stages a change to.
	// Empty for the enforced kinds themselves.
	stages string
}

var calicoPolicyDefinitions = []calicoPolicyDefinition{
	{kind: "NetworkPolicy", nodeKind: KindCalicoNetworkPolicy, resource: "networkpolicies", namespaced: true},
	{kind: "GlobalNetworkPolicy", nodeKind: KindCalicoGlobalNetworkPolicy, resource: "globalnetworkpolicies"},
	{kind: "StagedNetworkPolicy", nodeKind: KindCalicoStagedNetworkPolicy, resource: "stagednetworkpolicies", namespaced: true, staged: true, stages: "NetworkPolicy"},
	{kind: "StagedGlobalNetworkPolicy", nodeKind: KindCalicoStagedGlobalNetworkPolicy, resource: "stagedglobalnetworkpolicies", staged: true, stages: "GlobalNetworkPolicy"},
	{kind: "StagedKubernetesNetworkPolicy", nodeKind: KindCalicoStagedKubernetesNetworkPolicy, resource: "stagedkubernetesnetworkpolicies", namespaced: true, staged: true, kubernetes: true, stages: "NetworkPolicy"},
}

var calicoPolicyGroups = []string{calicoProjectGroup, calicoCRDGroup}

func isCalicoAPIGroup(group string) bool {
	return group == calicoProjectGroup || group == calicoCRDGroup
}

// CalicoPolicyKind describes one Calico policy API. Callers outside this
// package read it rather than keeping a second copy of the list, which would
// have to be found and updated whenever Calico adds a policy kind.
type CalicoPolicyKind struct {
	Kind       string
	Resource   string
	Namespaced bool
	Staged     bool
	// Kubernetes marks a staged policy carrying the Kubernetes NetworkPolicy
	// spec shape rather than Calico's, so it stages a change to a policy in
	// networking.k8s.io rather than to a Calico one.
	Kubernetes bool
	// Stages names the enforced kind a staged policy stages a change to.
	Stages string
}

// CalicoPolicyKinds returns the Calico policy APIs Radar reads.
func CalicoPolicyKinds() []CalicoPolicyKind {
	kinds := make([]CalicoPolicyKind, 0, len(calicoPolicyDefinitions))
	for _, definition := range calicoPolicyDefinitions {
		kinds = append(kinds, CalicoPolicyKind{
			Kind:       definition.kind,
			Resource:   definition.resource,
			Namespaced: definition.namespaced,
			Staged:     definition.staged,
			Kubernetes: definition.kubernetes,
			Stages:     definition.stages,
		})
	}
	return kinds
}

// CalicoAPIGroups returns the API groups Calico policies are served under, in
// the order they are preferred when both serve the same object.
func CalicoAPIGroups() []string {
	return append([]string(nil), calicoPolicyGroups...)
}

// IsCalicoPolicyKind reports whether kind is one of the topology's Calico
// policy pseudo-kinds. Core NetworkPolicy deliberately does not match.
func IsCalicoPolicyKind(kind NodeKind) bool {
	for _, definition := range calicoPolicyDefinitions {
		if definition.nodeKind == kind {
			return true
		}
	}
	return false
}

// calicoNodeAPIVersions returns every apiVersion a policy node was served
// under. A cluster running the Calico apiserver serves the same policy through
// both groups, and the two are authorized independently, so the node records
// all of them. Node data survives a JSON round trip on the SSE and hub paths,
// so both the native and the decoded shape have to be understood.
func calicoNodeAPIVersions(node *Node) []string {
	if node == nil || node.Data == nil {
		return nil
	}
	var versions []string
	switch recorded := node.Data["apiVersions"].(type) {
	case []string:
		versions = recorded
	case []any:
		for _, value := range recorded {
			if version, ok := value.(string); ok {
				versions = append(versions, version)
			}
		}
	}
	if len(versions) == 0 {
		if apiVersion, _ := node.Data["apiVersion"].(string); apiVersion != "" {
			versions = []string{apiVersion}
		}
	}
	return versions
}

// calicoNodeAPIGroups returns every Calico API group that served a policy node.
func calicoNodeAPIGroups(node *Node) []string {
	var groups []string
	for _, apiVersion := range calicoNodeAPIVersions(node) {
		if group := resourceid.GroupFromAPIVersion(apiVersion); group != "" {
			groups = append(groups, group)
		}
	}
	return groups
}

// CalicoPolicyRBACTuples returns the exact API identities for a Calico policy
// node — one per group that served it. A caller authorized for any one of them
// can genuinely read the policy, so the tuples are evaluated as alternatives.
func CalicoPolicyRBACTuples(node *Node) ([]SARTuple, bool) {
	if node == nil {
		return nil, false
	}
	definition, ok := calicoPolicyDefinitionForNodeKind(node.Kind)
	if !ok || node.Data == nil {
		return nil, false
	}
	namespace := nodeNamespaceFromData(node)
	seen := make(map[string]bool, 2)
	var tuples []SARTuple
	for _, group := range calicoNodeAPIGroups(node) {
		group = strings.ToLower(group)
		if !isCalicoAPIGroup(group) || seen[group] {
			continue
		}
		seen[group] = true
		tuples = append(tuples, SARTuple{
			Group:     group,
			Resource:  definition.resource,
			Namespace: namespace,
		})
	}
	if len(tuples) == 0 {
		return nil, false
	}
	return tuples, true
}

// isCalicoPolicyGVR reports whether a watched resource is one of the Calico
// policy kinds the builder renders explicitly.
func isCalicoPolicyGVR(gvr schema.GroupVersionResource) bool {
	if !isCalicoAPIGroup(gvr.Group) {
		return false
	}
	for _, definition := range calicoPolicyDefinitions {
		if definition.resource == gvr.Resource {
			return true
		}
	}
	return false
}

func calicoPolicyDefinitionForNodeKind(kind NodeKind) (calicoPolicyDefinition, bool) {
	for _, definition := range calicoPolicyDefinitions {
		if definition.nodeKind == kind {
			return definition, true
		}
	}
	return calicoPolicyDefinition{}, false
}

// CalicoPolicyRBACTuples returns the distinct exact Calico policy identities
// present in the topology.
func (t *Topology) CalicoPolicyRBACTuples() []SARTuple {
	if t == nil {
		return nil
	}
	seen := make(map[SARTuple]bool)
	var tuples []SARTuple
	for i := range t.Nodes {
		nodeTuples, ok := CalicoPolicyRBACTuples(&t.Nodes[i])
		if !ok {
			continue
		}
		for _, tuple := range nodeTuples {
			if seen[tuple] {
				continue
			}
			seen[tuple] = true
			tuples = append(tuples, tuple)
		}
	}
	return tuples
}

// StripCalicoPoliciesExcept removes Calico policy nodes the caller cannot list
// under any of the groups that served them. Malformed Calico nodes fail closed.
// Native NetworkPolicy nodes are intentionally untouched.
func (t *Topology) StripCalicoPoliciesExcept(allowed map[SARTuple]bool) {
	if t == nil {
		return
	}
	deny := make(map[string]bool)
	for i := range t.Nodes {
		node := &t.Nodes[i]
		if !IsCalicoPolicyKind(node.Kind) {
			continue
		}
		tuples, ok := CalicoPolicyRBACTuples(node)
		if !ok {
			deny[node.ID] = true
			continue
		}
		authorized := false
		for _, tuple := range tuples {
			if allowed[tuple] {
				authorized = true
				break
			}
		}
		if !authorized {
			deny[node.ID] = true
			continue
		}
		readvertiseCalicoPolicy(node, func(tuple SARTuple) bool { return allowed[tuple] })
	}
	t.StripNodeIDs(deny)
}

// ReadvertiseCalicoPolicyNodes points every Calico policy node at an API group
// the caller can address, given the same authorizer that admitted it. Callers
// that filter node by node rather than through StripCalicoPoliciesExcept — the
// neighborhood paths — need this too: a node the caller may read through one
// serving group is useless if it advertises the other.
func ReadvertiseCalicoPolicyNodes(nodes []Node, authorize func(SARTuple) bool) {
	if authorize == nil {
		return
	}
	for i := range nodes {
		if IsCalicoPolicyKind(nodes[i].Kind) {
			readvertiseCalicoPolicy(&nodes[i], authorize)
		}
	}
}

// readvertiseCalicoPolicy points a surviving node at an API group the caller is
// authorized for. The node advertises one apiVersion and the UI builds its
// detail fetch from it, so a caller who can read the policy only through the
// other serving group would otherwise follow the node to a 403. The node's data
// map is shared between per-user views of one build, so it is replaced rather
// than written through.
func readvertiseCalicoPolicy(node *Node, authorize func(SARTuple) bool) {
	definition, ok := calicoPolicyDefinitionForNodeKind(node.Kind)
	if !ok || node.Data == nil {
		return
	}
	namespace := nodeNamespaceFromData(node)
	tupleFor := func(apiVersion string) SARTuple {
		return SARTuple{
			Group:     strings.ToLower(resourceid.GroupFromAPIVersion(apiVersion)),
			Resource:  definition.resource,
			Namespace: namespace,
		}
	}
	current, _ := node.Data["apiVersion"].(string)
	if authorize(tupleFor(current)) {
		return
	}
	for _, apiVersion := range calicoNodeAPIVersions(node) {
		if !authorize(tupleFor(apiVersion)) {
			continue
		}
		replacement := make(map[string]any, len(node.Data))
		for key, value := range node.Data {
			replacement[key] = value
		}
		replacement["apiVersion"] = apiVersion
		node.Data = replacement
		return
	}
}

func calicoPolicyNodeID(kind NodeKind, namespace, name string) string {
	return strings.ToLower(string(kind)) + "/" + namespace + "/" + name
}

func calicoPolicyIdentity(kind NodeKind, namespace, name string) string {
	return string(kind) + "\x00" + namespace + "\x00" + name
}

// recordCalicoPolicyGroup folds a policy that a second API group also serves
// into the node already built for it, and reports whether it did. A cluster
// running the Calico apiserver serves every policy under both
// projectcalico.org and crd.projectcalico.org — they are two views of one
// stored object, so rendering one node per group would double every policy and
// every edge it draws. The extra group is kept in node data because the two are
// authorized independently.
func recordCalicoPolicyGroup(node *Node, apiVersion string) {
	if node.Data == nil {
		node.Data = map[string]any{}
	}
	versions, _ := node.Data["apiVersions"].([]string)
	for _, existing := range versions {
		if existing == apiVersion {
			return
		}
	}
	node.Data["apiVersions"] = append(versions, apiVersion)
}

type calicoWorkload struct {
	id             string
	namespace      string
	labels         map[string]string
	endpointLabels map[string]string
	serviceAccount string
}

func newCalicoWorkload(id, namespace string, workloadLabels map[string]string, serviceAccount string) calicoWorkload {
	return calicoWorkload{
		id:             id,
		namespace:      namespace,
		labels:         workloadLabels,
		endpointLabels: netpol.CalicoEndpointLabels(namespace, workloadLabels, serviceAccount),
		serviceAccount: serviceAccount,
	}
}

func calicoNamespaceLabels(provider ResourceProvider) map[string]map[string]string {
	namespaceProvider, ok := provider.(NamespaceProvider)
	if !ok {
		return nil
	}
	namespaces, err := namespaceProvider.Namespaces()
	if err != nil {
		return nil
	}
	result := make(map[string]map[string]string, len(namespaces))
	for _, namespace := range namespaces {
		if namespace != nil {
			result[namespace.Name] = netpol.CalicoNamespaceLabels(namespace)
		}
	}
	return result
}

func calicoServiceAccounts(provider ResourceProvider) map[string]map[string]string {
	serviceAccountProvider, ok := provider.(ServiceAccountProvider)
	if !ok {
		return nil
	}
	serviceAccounts, err := serviceAccountProvider.ServiceAccounts()
	if err != nil {
		return nil
	}
	result := make(map[string]map[string]string, len(serviceAccounts))
	for _, serviceAccount := range serviceAccounts {
		if serviceAccount != nil {
			result[serviceAccount.Namespace+"/"+serviceAccount.Name] = netpol.CalicoServiceAccountLabels(serviceAccount)
		}
	}
	return result
}

func calicoPolicyTypes(policy *unstructured.Unstructured) []string {
	values, found, _ := unstructured.NestedStringSlice(policy.Object, "spec", "types")
	if found {
		return values
	}
	values, found, _ = unstructured.NestedStringSlice(policy.Object, "spec", "policyTypes")
	if found {
		return values
	}
	value, found, _ := unstructured.NestedString(policy.Object, "spec", "policyTypes")
	if found && value != "" {
		return []string{value}
	}
	return nil
}

func calicoPolicyField(policy *unstructured.Unstructured, field string) any {
	spec, _, _ := unstructured.NestedMap(policy.Object, "spec")
	if spec == nil {
		return nil
	}
	return spec[field]
}

func calicoPolicySelectorValue(policy *unstructured.Unstructured, definition calicoPolicyDefinition) any {
	if !definition.kubernetes {
		return calicoPolicyField(policy, "selector")
	}
	selector, valid := netpol.CalicoKubernetesPodSelector(policy)
	if !valid {
		return "invalid selector"
	}
	if selector.String() == "" {
		return nil
	}
	return selector.String()
}

// calicoPolicyMatchesAllWorkloads reports whether a policy selects every
// workload in its scope, which lets the builder record coverage without drawing
// an edge per workload. A namespace or service-account selector narrows the
// policy, so an all() endpoint selector alongside one covers a subset — the
// per-workload match has to decide, not this shortcut.
func calicoPolicyMatchesAllWorkloads(policy *unstructured.Unstructured, definition calicoPolicyDefinition) bool {
	if definition.kubernetes {
		selector, valid := netpol.CalicoKubernetesPodSelector(policy)
		return valid && selector.String() == ""
	}
	for _, field := range []string{"namespaceSelector", "serviceAccountSelector"} {
		value, found, err := unstructured.NestedString(policy.Object, "spec", field)
		if err != nil || (found && !netpol.IsCalicoAllSelector(value)) {
			return false
		}
	}
	selector, found, err := unstructured.NestedString(policy.Object, "spec", "selector")
	return err == nil && (!found || netpol.IsCalicoAllSelector(selector))
}

func (b *Builder) addCalicoPolicyNodes(
	nodes []Node,
	edges []Edge,
	opts BuildOptions,
	warnings *[]string,
	deployments []*appsv1.Deployment,
	statefulsets []*appsv1.StatefulSet,
	daemonsets []*appsv1.DaemonSet,
	deploymentIDs, statefulSetIDs map[string]string,
) ([]Node, []Edge) {
	if b.dynamic == nil {
		return nodes, edges
	}

	namespaceLabels := calicoNamespaceLabels(b.provider)
	serviceAccounts := calicoServiceAccounts(b.provider)
	// Only this function creates Calico policy nodes, so an index it fills as it
	// goes is complete, and the second API group's pass becomes a lookup rather
	// than a scan of every node in the graph.
	policyNodeIndex := make(map[string]int)
	targets := make([]calicoWorkload, 0, len(deployments)+len(statefulsets)+len(daemonsets))
	for _, deployment := range deployments {
		if id := deploymentIDs[deployment.Namespace+"/"+deployment.Name]; id != "" {
			sa := deployment.Spec.Template.Spec.ServiceAccountName
			if sa == "" {
				sa = "default"
			}
			targets = append(targets, newCalicoWorkload(id, deployment.Namespace, deployment.Spec.Template.Labels, sa))
		}
	}
	for _, statefulSet := range statefulsets {
		if id := statefulSetIDs[statefulSet.Namespace+"/"+statefulSet.Name]; id != "" {
			sa := statefulSet.Spec.Template.Spec.ServiceAccountName
			if sa == "" {
				sa = "default"
			}
			targets = append(targets, newCalicoWorkload(id, statefulSet.Namespace, statefulSet.Spec.Template.Labels, sa))
		}
	}
	for _, daemonSet := range daemonsets {
		id := ""
		if opts.MatchesNamespaceFilter(daemonSet.Namespace) {
			id = "daemonset/" + daemonSet.Namespace + "/" + daemonSet.Name
		}
		if id != "" {
			sa := daemonSet.Spec.Template.Spec.ServiceAccountName
			if sa == "" {
				sa = "default"
			}
			targets = append(targets, newCalicoWorkload(id, daemonSet.Namespace, daemonSet.Spec.Template.Labels, sa))
		}
	}

	for _, group := range calicoPolicyGroups {
		for _, definition := range calicoPolicyDefinitions {
			gvr, found := b.dynamic.GetGVRWithGroup(definition.kind, group)
			if !found {
				continue
			}
			var policies []*unstructured.Unstructured
			var err error
			if definition.namespaced {
				policies, err = b.dynamic.ListNamespaces(gvr, opts.Namespaces)
			} else {
				policies, err = b.dynamic.List(gvr, "")
			}
			if err != nil {
				message := fmt.Sprintf("Failed to list Calico %ss (%s): %v", definition.kind, group, err)
				log.Printf("WARNING [topology] %s", message)
				*warnings = append(*warnings, message)
				continue
			}

			for _, policy := range policies {
				if policy == nil {
					continue
				}
				namespace := policy.GetNamespace()
				if definition.namespaced && !opts.MatchesNamespaceFilter(namespace) {
					continue
				}
				apiVersion := policy.GetAPIVersion()
				if apiVersion == "" {
					apiVersion = group + "/" + gvr.Version
				}
				nodeData := map[string]any{
					"namespace":   namespace,
					"labels":      policy.GetLabels(),
					"apiVersion":  apiVersion,
					"policyTypes": calicoPolicyTypes(policy),
					"selector":    calicoPolicySelectorValue(policy, definition),
				}
				if definition.kubernetes {
					if podSelector := calicoPolicyField(policy, "podSelector"); podSelector != nil {
						nodeData["podSelector"] = podSelector
					}
				}
				for _, field := range []string{"namespaceSelector", "serviceAccountSelector", "tier", "order", "stagedAction", "preDNAT", "applyOnForward", "doNotTrack"} {
					if value := calicoPolicyField(policy, field); value != nil {
						nodeData[field] = value
					}
				}

				identity := calicoPolicyIdentity(definition.nodeKind, namespace, policy.GetName())
				if index, built := policyNodeIndex[identity]; built {
					recordCalicoPolicyGroup(&nodes[index], apiVersion)
					continue
				}
				nodeData["apiVersions"] = []string{apiVersion}
				nodeID := calicoPolicyNodeID(definition.nodeKind, namespace, policy.GetName())
				policyNodeIndex[identity] = len(nodes)
				status := StatusHealthy
				if definition.staged {
					status = StatusNeutral
				}
				nodes = append(nodes, Node{ID: nodeID, Kind: definition.nodeKind, Name: policy.GetName(), Status: status, Data: nodeData})

				if definition.staged && !netpol.CalicoStagedActionPreviewsProtection(policy) {
					continue
				}

				matcher := netpol.CompileCalicoPolicyMatcher(policy)
				endpointAll := calicoPolicyMatchesAllWorkloads(policy, definition)
				if endpointAll {
					nodeData["matchesAllPods"] = true
				}
				var coverage []string
				for _, target := range targets {
					if definition.namespaced && target.namespace != namespace {
						continue
					}
					workloadLabels := target.endpointLabels
					if definition.kubernetes {
						workloadLabels = target.labels
					}
					matched, selectorValid := matcher.Matches(
						workloadLabels,
						namespaceLabels[target.namespace],
						target.serviceAccount,
						serviceAccounts[target.namespace+"/"+target.serviceAccount],
					)
					if !selectorValid || !matched {
						continue
					}
					if endpointAll {
						coverage = append(coverage, target.id)
						continue
					}
					edges = append(edges, Edge{ID: nodeID + "-to-" + target.id, Source: nodeID, Target: target.id, Type: EdgeProtects, Partial: definition.staged})
				}
				if len(coverage) > 0 {
					nodeData["policyCoverageWorkloads"] = coverage
				}
			}
		}
	}
	return nodes, edges
}
