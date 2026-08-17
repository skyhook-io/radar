package topology

import (
	"log"
	"regexp"
	"strings"
	"unicode"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
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
}

var calicoPolicyDefinitions = []calicoPolicyDefinition{
	{kind: "NetworkPolicy", nodeKind: KindCalicoNetworkPolicy, resource: "networkpolicies", namespaced: true},
	{kind: "GlobalNetworkPolicy", nodeKind: KindCalicoGlobalNetworkPolicy, resource: "globalnetworkpolicies"},
	{kind: "StagedNetworkPolicy", nodeKind: KindCalicoStagedNetworkPolicy, resource: "stagednetworkpolicies", namespaced: true, staged: true},
	{kind: "StagedGlobalNetworkPolicy", nodeKind: KindCalicoStagedGlobalNetworkPolicy, resource: "stagedglobalnetworkpolicies", staged: true},
	{kind: "StagedKubernetesNetworkPolicy", nodeKind: KindCalicoStagedKubernetesNetworkPolicy, resource: "stagedkubernetesnetworkpolicies", namespaced: true, staged: true, kubernetes: true},
}

var calicoPolicyGroups = []string{calicoProjectGroup, calicoCRDGroup}

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

// CalicoPolicyRBACTuple returns the exact API identity for a Calico policy
// node. The group is part of the identity because projectcalico.org and
// crd.projectcalico.org can serve the same kind, namespace, and name.
func CalicoPolicyRBACTuple(node *Node) (SARTuple, bool) {
	if node == nil {
		return SARTuple{}, false
	}
	definition, ok := calicoPolicyDefinitionForNodeKind(node.Kind)
	if !ok || node.Data == nil {
		return SARTuple{}, false
	}
	apiVersion, _ := node.Data["apiVersion"].(string)
	group := strings.ToLower(APIVersionGroup(apiVersion))
	if group != calicoProjectGroup && group != calicoCRDGroup {
		return SARTuple{}, false
	}
	return SARTuple{
		Group:     group,
		Resource:  definition.resource,
		Namespace: nodeNamespaceFromData(node),
	}, true
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
		tuple, ok := CalicoPolicyRBACTuple(&t.Nodes[i])
		if !ok || seen[tuple] {
			continue
		}
		seen[tuple] = true
		tuples = append(tuples, tuple)
	}
	return tuples
}

// StripCalicoPoliciesExcept removes Calico policy nodes whose exact API
// identity is not in allowed. Malformed Calico nodes fail closed. Native
// NetworkPolicy nodes are intentionally untouched.
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
		tuple, ok := CalicoPolicyRBACTuple(node)
		if !ok || !allowed[tuple] {
			deny[node.ID] = true
		}
	}
	t.StripNodeIDs(deny)
}

func calicoPolicyDefinitionForKind(kind string) (calicoPolicyDefinition, bool) {
	for _, definition := range calicoPolicyDefinitions {
		if strings.EqualFold(definition.kind, kind) || strings.EqualFold(string(definition.nodeKind), kind) {
			return definition, true
		}
	}
	return calicoPolicyDefinition{}, false
}

func calicoPolicyNodeID(kind NodeKind, namespace, name string) string {
	return strings.ToLower(string(kind)) + "/" + namespace + "/" + name
}

// reserveCalicoPolicyNodeID keeps the common ID compact, but adds the API
// group only when the legacy and modern Calico APIs expose the same identity.
// Both groups can be served during an upgrade, so dropping the group from the
// ID unconditionally would make one policy overwrite the other in indexes.
func reserveCalicoPolicyNodeID(nodes []Node, edges []Edge, kind NodeKind, namespace, name, group string) (string, []Node, []Edge, bool) {
	baseID := calicoPolicyNodeID(kind, namespace, name)
	duplicate := false
	for i := range nodes {
		node := &nodes[i]
		if node.Kind != kind || node.Name != name || nodeNamespaceFromData(node) != namespace {
			continue
		}
		existingGroup := nodeAPIGroupFromData(node)
		if existingGroup == group {
			return "", nodes, edges, true
		}
		duplicate = true
		if node.ID == baseID {
			qualifiedID := baseID + "/" + existingGroup
			for edgeIndex := range edges {
				edge := &edges[edgeIndex]
				oldSource, oldTarget := edge.Source, edge.Target
				if oldSource == baseID {
					edge.Source = qualifiedID
				}
				if oldTarget == baseID {
					edge.Target = qualifiedID
				}
				if edge.ID == oldSource+"-to-"+oldTarget {
					edge.ID = edge.Source + "-to-" + edge.Target
				}
			}
			node.ID = qualifiedID
		}
	}
	if duplicate {
		return baseID + "/" + group, nodes, edges, false
	}
	return baseID, nodes, edges, false
}

type calicoWorkload struct {
	id             string
	namespace      string
	labels         map[string]string
	serviceAccount string
}

// CalicoEndpointLabels returns the labels Calico exposes for a Kubernetes
// endpoint, including the labels it adds automatically to every workload.
func CalicoEndpointLabels(namespace string, workloadLabels map[string]string) map[string]string {
	endpointLabels := make(map[string]string, len(workloadLabels)+2)
	for key, value := range workloadLabels {
		endpointLabels[key] = value
	}
	endpointLabels["projectcalico.org/namespace"] = namespace
	endpointLabels["projectcalico.org/orchestrator"] = "k8s"
	return endpointLabels
}

type calicoSelectorTokenKind uint8

const (
	calicoTokenWord calicoSelectorTokenKind = iota
	calicoTokenString
	calicoTokenOperator
	calicoTokenBang
	calicoTokenLParen
	calicoTokenRParen
	calicoTokenLBrace
	calicoTokenRBrace
	calicoTokenComma
	calicoTokenEOF
)

type calicoSelectorToken struct {
	kind calicoSelectorTokenKind
	text string
}

type calicoSelectorExpr func(map[string]string) bool

func compileCalicoSelector(expression string) (calicoSelectorExpr, bool) {
	if strings.TrimSpace(expression) == "" {
		return func(map[string]string) bool { return true }, true
	}
	tokens, ok := lexCalicoSelector(expression)
	if !ok {
		return nil, false
	}
	parser := calicoSelectorParser{tokens: tokens}
	expr, ok := parser.parseOr()
	if !ok || parser.peek().kind != calicoTokenEOF {
		return nil, false
	}
	return expr, true
}

func isCalicoAllSelector(expression string) bool {
	trimmed := strings.TrimSpace(expression)
	if trimmed == "" {
		return true
	}
	compact := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, trimmed)
	return strings.EqualFold(compact, "all()")
}

func lexCalicoSelector(expression string) ([]calicoSelectorToken, bool) {
	tokens := make([]calicoSelectorToken, 0, len(expression)/2)
	for i := 0; i < len(expression); {
		if unicode.IsSpace(rune(expression[i])) {
			i++
			continue
		}
		switch expression[i] {
		case '(':
			tokens = append(tokens, calicoSelectorToken{kind: calicoTokenLParen, text: "("})
			i++
		case ')':
			tokens = append(tokens, calicoSelectorToken{kind: calicoTokenRParen, text: ")"})
			i++
		case '{':
			tokens = append(tokens, calicoSelectorToken{kind: calicoTokenLBrace, text: "{"})
			i++
		case '}':
			tokens = append(tokens, calicoSelectorToken{kind: calicoTokenRBrace, text: "}"})
			i++
		case ',':
			tokens = append(tokens, calicoSelectorToken{kind: calicoTokenComma, text: ","})
			i++
		case '!':
			if i+1 < len(expression) && expression[i+1] == '=' {
				tokens = append(tokens, calicoSelectorToken{kind: calicoTokenOperator, text: "!="})
				i += 2
			} else {
				tokens = append(tokens, calicoSelectorToken{kind: calicoTokenBang, text: "!"})
				i++
			}
		case '=':
			if i+1 >= len(expression) || expression[i+1] != '=' {
				return nil, false
			}
			tokens = append(tokens, calicoSelectorToken{kind: calicoTokenOperator, text: "=="})
			i += 2
		case '&', '|':
			if i+1 >= len(expression) || expression[i+1] != expression[i] {
				return nil, false
			}
			tokens = append(tokens, calicoSelectorToken{kind: calicoTokenOperator, text: expression[i : i+2]})
			i += 2
		case '\'', '"':
			value, next, ok := readCalicoString(expression, i)
			if !ok {
				return nil, false
			}
			tokens = append(tokens, calicoSelectorToken{kind: calicoTokenString, text: value})
			i = next
		default:
			if !isCalicoWordRune(rune(expression[i])) {
				return nil, false
			}
			start := i
			for i < len(expression) && isCalicoWordRune(rune(expression[i])) {
				i++
			}
			tokens = append(tokens, calicoSelectorToken{kind: calicoTokenWord, text: expression[start:i]})
		}
	}
	tokens = append(tokens, calicoSelectorToken{kind: calicoTokenEOF})
	return tokens, true
}

func isCalicoWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("_-.:/%", r)
}

func readCalicoString(expression string, start int) (string, int, bool) {
	quote := expression[start]
	var value strings.Builder
	for i := start + 1; i < len(expression); i++ {
		ch := expression[i]
		if ch == quote {
			return value.String(), i + 1, true
		}
		if ch == '\\' {
			if i+1 >= len(expression) {
				return "", 0, false
			}
			next := expression[i+1]
			switch next {
			case 'n':
				value.WriteByte('\n')
			case 'r':
				value.WriteByte('\r')
			case 't':
				value.WriteByte('\t')
			default:
				value.WriteByte(next)
			}
			i++
			continue
		}
		value.WriteByte(ch)
	}
	return "", 0, false
}

type calicoSelectorParser struct {
	tokens []calicoSelectorToken
	pos    int
}

func (p *calicoSelectorParser) peek() calicoSelectorToken {
	if p.pos >= len(p.tokens) {
		return calicoSelectorToken{kind: calicoTokenEOF}
	}
	return p.tokens[p.pos]
}

func (p *calicoSelectorParser) take() calicoSelectorToken {
	token := p.peek()
	if p.pos < len(p.tokens) {
		p.pos++
	}
	return token
}

func (p *calicoSelectorParser) accept(kind calicoSelectorTokenKind, text string) bool {
	token := p.peek()
	if token.kind != kind || (text != "" && token.text != text) {
		return false
	}
	p.pos++
	return true
}

func (p *calicoSelectorParser) parseOr() (calicoSelectorExpr, bool) {
	left, ok := p.parseAnd()
	if !ok {
		return nil, false
	}
	for p.accept(calicoTokenOperator, "||") {
		right, rightOK := p.parseAnd()
		if !rightOK {
			return nil, false
		}
		previous := left
		left = func(labels map[string]string) bool { return previous(labels) || right(labels) }
	}
	return left, true
}

func (p *calicoSelectorParser) parseAnd() (calicoSelectorExpr, bool) {
	left, ok := p.parseUnary()
	if !ok {
		return nil, false
	}
	for p.accept(calicoTokenOperator, "&&") {
		right, rightOK := p.parseUnary()
		if !rightOK {
			return nil, false
		}
		previous := left
		left = func(labels map[string]string) bool { return previous(labels) && right(labels) }
	}
	return left, true
}

func (p *calicoSelectorParser) parseUnary() (calicoSelectorExpr, bool) {
	if p.accept(calicoTokenBang, "!") {
		expr, ok := p.parseUnary()
		if !ok {
			return nil, false
		}
		return func(labels map[string]string) bool { return !expr(labels) }, true
	}
	return p.parsePrimary()
}

func (p *calicoSelectorParser) parsePrimary() (calicoSelectorExpr, bool) {
	if p.accept(calicoTokenLParen, "(") {
		expr, ok := p.parseOr()
		if !ok || !p.accept(calicoTokenRParen, ")") {
			return nil, false
		}
		return expr, true
	}
	return p.parsePredicate()
}

func (p *calicoSelectorParser) parsePredicate() (calicoSelectorExpr, bool) {
	key := p.take()
	if key.kind != calicoTokenWord {
		return nil, false
	}
	if strings.EqualFold(key.text, "all") {
		if !p.accept(calicoTokenLParen, "(") || !p.accept(calicoTokenRParen, ")") {
			return nil, false
		}
		return func(map[string]string) bool { return true }, true
	}
	if strings.EqualFold(key.text, "has") {
		if !p.accept(calicoTokenLParen, "(") {
			return nil, false
		}
		argument := p.take()
		if argument.kind != calicoTokenWord || !p.accept(calicoTokenRParen, ")") {
			return nil, false
		}
		return func(labels map[string]string) bool {
			_, ok := labels[argument.text]
			return ok
		}, true
	}

	op := p.take()
	operator := strings.ToLower(op.text)
	if op.kind == calicoTokenWord && operator == "not" {
		in := p.take()
		if in.kind != calicoTokenWord || strings.ToLower(in.text) != "in" {
			return nil, false
		}
		operator = "not in"
	}
	if op.kind != calicoTokenOperator && !(op.kind == calicoTokenWord && (operator == "in" || operator == "not in" || operator == "contains" || operator == "starts" || operator == "ends" || operator == "matches")) {
		return nil, false
	}
	if operator == "starts" || operator == "ends" {
		with := p.take()
		if with.kind != calicoTokenWord || strings.ToLower(with.text) != "with" {
			return nil, false
		}
		operator += " with"
	}

	if operator == "in" || operator == "not in" {
		values, ok := p.parseSet()
		if !ok {
			return nil, false
		}
		return func(labels map[string]string) bool {
			value, exists := labels[key.text]
			_, contained := values[value]
			if operator == "in" {
				return exists && contained
			}
			return !exists || !contained
		}, true
	}

	valueToken := p.take()
	if valueToken.kind != calicoTokenString && valueToken.kind != calicoTokenWord {
		return nil, false
	}
	value := valueToken.text
	if operator == "matches" {
		pattern, err := regexp.Compile(value)
		if err != nil {
			return nil, false
		}
		return func(labels map[string]string) bool {
			candidate, exists := labels[key.text]
			return exists && pattern.MatchString(candidate)
		}, true
	}
	return func(labels map[string]string) bool {
		candidate, exists := labels[key.text]
		switch operator {
		case "==":
			return exists && candidate == value
		case "!=":
			return !exists || candidate != value
		case "contains":
			return exists && strings.Contains(candidate, value)
		case "starts with":
			return exists && strings.HasPrefix(candidate, value)
		case "ends with":
			return exists && strings.HasSuffix(candidate, value)
		default:
			return false
		}
	}, true
}

func (p *calicoSelectorParser) parseSet() (map[string]bool, bool) {
	if !p.accept(calicoTokenLBrace, "{") {
		return nil, false
	}
	values := map[string]bool{}
	if p.accept(calicoTokenRBrace, "}") {
		return values, true
	}
	for {
		value := p.take()
		if value.kind != calicoTokenString && value.kind != calicoTokenWord {
			return nil, false
		}
		values[value.text] = true
		if p.accept(calicoTokenRBrace, "}") {
			return values, true
		}
		if !p.accept(calicoTokenComma, ",") {
			return nil, false
		}
	}
}

func calicoServiceAccountLabels(serviceAccount *corev1.ServiceAccount) map[string]string {
	if serviceAccount == nil {
		return nil
	}
	labels := make(map[string]string, len(serviceAccount.Labels)+2)
	for key, value := range serviceAccount.Labels {
		labels[key] = value
	}
	labels["projectcalico.org/name"] = serviceAccount.Name
	labels["kubernetes.io/service-account.name"] = serviceAccount.Name
	return labels
}

func calicoPolicyMatchesWorkload(policy *unstructured.Unstructured, definition calicoPolicyDefinition, workload calicoWorkload, namespaceLabels map[string]string, serviceAccounts map[string]map[string]string) (matched, endpointSelectorValid bool) {
	if definition.kubernetes {
		return calicoKubernetesPolicyMatchesWorkload(policy, workload.labels)
	}
	return CalicoPolicyMatchesWorkload(
		policy,
		CalicoEndpointLabels(workload.namespace, workload.labels),
		namespaceLabels,
		workload.serviceAccount,
		serviceAccounts[workload.namespace+"/"+workload.serviceAccount],
	)
}

func CalicoPolicyMatchesWorkload(policy *unstructured.Unstructured, workloadLabels, namespaceLabels map[string]string, serviceAccountName string, serviceAccountLabels map[string]string) (matched, endpointSelectorValid bool) {
	if isStagedKubernetesNetworkPolicy(policy) {
		return calicoKubernetesPolicyMatchesWorkload(policy, workloadLabels)
	}
	selector, found, err := unstructured.NestedString(policy.Object, "spec", "selector")
	if err != nil {
		return false, false
	}
	if !found {
		selector = ""
	}
	endpointMatcher, valid := compileCalicoSelector(selector)
	if !valid {
		return false, false
	}
	if !endpointMatcher(workloadLabels) {
		return false, true
	}

	namespaceSelector, found, err := unstructured.NestedString(policy.Object, "spec", "namespaceSelector")
	if err != nil {
		return false, true
	}
	if found && !isCalicoAllSelector(namespaceSelector) {
		matcher, selectorOK := compileCalicoSelector(namespaceSelector)
		if !selectorOK || namespaceLabels == nil || !matcher(namespaceLabels) {
			return false, true
		}
	}

	serviceAccountSelector, found, err := unstructured.NestedString(policy.Object, "spec", "serviceAccountSelector")
	if err != nil {
		return false, true
	}
	if found && !isCalicoAllSelector(serviceAccountSelector) {
		matcher, selectorOK := compileCalicoSelector(serviceAccountSelector)
		if !selectorOK || serviceAccountName == "" {
			return false, true
		}
		if serviceAccountLabels == nil || !matcher(serviceAccountLabels) {
			return false, true
		}
	}

	return true, true
}

func isStagedKubernetesNetworkPolicy(policy *unstructured.Unstructured) bool {
	return policy != nil && strings.EqualFold(policy.GetKind(), "StagedKubernetesNetworkPolicy")
}

func calicoKubernetesPolicyMatchesWorkload(policy *unstructured.Unstructured, workloadLabels map[string]string) (bool, bool) {
	selector, valid := calicoKubernetesPodSelector(policy)
	if !valid {
		return false, false
	}
	return selector.Matches(labels.Set(workloadLabels)), true
}

func calicoKubernetesPodSelector(policy *unstructured.Unstructured) (labels.Selector, bool) {
	selectorMap, found, err := unstructured.NestedMap(policy.Object, "spec", "podSelector")
	if err != nil {
		return nil, false
	}
	if !found || len(selectorMap) == 0 {
		return labels.Everything(), true
	}
	var labelSelector metav1.LabelSelector
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(selectorMap, &labelSelector); err != nil {
		return nil, false
	}
	selector, err := metav1.LabelSelectorAsSelector(&labelSelector)
	if err != nil {
		return nil, false
	}
	return selector, true
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
			labels := make(map[string]string, len(namespace.Labels)+2)
			for key, value := range namespace.Labels {
				labels[key] = value
			}
			labels["kubernetes.io/metadata.name"] = namespace.Name
			labels["projectcalico.org/name"] = namespace.Name
			result[namespace.Name] = labels
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
			result[serviceAccount.Namespace+"/"+serviceAccount.Name] = calicoServiceAccountLabels(serviceAccount)
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
	selector, valid := calicoKubernetesPodSelector(policy)
	if !valid {
		return "invalid selector"
	}
	if selector.String() == "" {
		return nil
	}
	return selector.String()
}

func calicoPolicyMatchesAllWorkloads(policy *unstructured.Unstructured, definition calicoPolicyDefinition) bool {
	if definition.kubernetes {
		selector, valid := calicoKubernetesPodSelector(policy)
		return valid && selector.String() == ""
	}
	selector, found, err := unstructured.NestedString(policy.Object, "spec", "selector")
	return err == nil && (!found || isCalicoAllSelector(selector))
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
	targets := make([]calicoWorkload, 0, len(deployments)+len(statefulsets)+len(daemonsets))
	for _, deployment := range deployments {
		if id := deploymentIDs[deployment.Namespace+"/"+deployment.Name]; id != "" {
			sa := deployment.Spec.Template.Spec.ServiceAccountName
			if sa == "" {
				sa = "default"
			}
			targets = append(targets, calicoWorkload{id: id, namespace: deployment.Namespace, labels: deployment.Spec.Template.Labels, serviceAccount: sa})
		}
	}
	for _, statefulSet := range statefulsets {
		if id := statefulSetIDs[statefulSet.Namespace+"/"+statefulSet.Name]; id != "" {
			sa := statefulSet.Spec.Template.Spec.ServiceAccountName
			if sa == "" {
				sa = "default"
			}
			targets = append(targets, calicoWorkload{id: id, namespace: statefulSet.Namespace, labels: statefulSet.Spec.Template.Labels, serviceAccount: sa})
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
			targets = append(targets, calicoWorkload{id: id, namespace: daemonSet.Namespace, labels: daemonSet.Spec.Template.Labels, serviceAccount: sa})
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
				message := "Failed to list " + definition.kind + "s (" + group + "): " + err.Error()
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

				nodeID, updatedNodes, updatedEdges, duplicate := reserveCalicoPolicyNodeID(
					nodes, edges, definition.nodeKind, namespace, policy.GetName(), group,
				)
				nodes, edges = updatedNodes, updatedEdges
				if duplicate {
					continue
				}
				status := StatusHealthy
				if definition.staged {
					status = StatusNeutral
				}
				nodes = append(nodes, Node{ID: nodeID, Kind: definition.nodeKind, Name: policy.GetName(), Status: status, Data: nodeData})

				endpointAll := calicoPolicyMatchesAllWorkloads(policy, definition)
				if endpointAll {
					nodeData["matchesAllPods"] = true
				}
				var coverage []string
				for _, target := range targets {
					if definition.namespaced && target.namespace != namespace {
						continue
					}
					matched, selectorValid := calicoPolicyMatchesWorkload(policy, definition, target, namespaceLabels[target.namespace], serviceAccounts)
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
