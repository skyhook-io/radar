package topology

import (
	"fmt"
	"log"
	"regexp"
	"strings"
	"unicode"

	"github.com/skyhook-io/radar/pkg/resourceid"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
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
		endpointLabels: CalicoEndpointLabels(namespace, workloadLabels),
		serviceAccount: serviceAccount,
	}
}

func calicoStagedAction(policy *unstructured.Unstructured) string {
	if policy == nil {
		return ""
	}
	action, _, _ := unstructured.NestedString(policy.Object, "spec", "stagedAction")
	return strings.ToLower(action)
}

// CalicoStagedActionPreviewsProtection reports whether a staged policy is a
// preview of protection that would exist once promoted. Delete is not: it
// previews removing a policy, and the Calico API requires its spec to be
// otherwise empty, so its absent selector would otherwise read as "selects every
// workload". Ignore is not either: the staged policy is skipped, so it previews
// nothing. Any other action, including one Calico adds later, is treated as a
// preview — omitting a real preview is the worse error.
func CalicoStagedActionPreviewsProtection(policy *unstructured.Unstructured) bool {
	switch calicoStagedAction(policy) {
	case "delete", "ignore":
		return false
	default:
		return policy != nil
	}
}

// CalicoStagedActionStagesRemoval reports whether promoting a staged policy
// would REMOVE the enforced policy it names. Only Delete does. This is a
// different question from whether the staged policy previews protection —
// Ignore answers no to both, and conflating them makes an ignored policy look
// like it takes protection away.
func CalicoStagedActionStagesRemoval(policy *unstructured.Unstructured) bool {
	return calicoStagedAction(policy) == "delete"
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

// CalicoPolicyMatcher is one policy's selectors, compiled. Compiling a selector
// costs roughly eighty times what evaluating it does, and every policy is
// evaluated against every workload, so the compile must happen once per policy
// rather than once per pair.
type CalicoPolicyMatcher struct {
	kubernetes         bool
	kubernetesSelector labels.Selector

	endpoint      calicoSelectorExpr
	endpointValid bool

	// blocked records a selector that is present but unusable. It cannot match
	// anything, yet it says nothing about the endpoint selector's validity.
	blocked bool

	namespaceSelector      calicoSelectorExpr
	serviceAccountSelector calicoSelectorExpr
}

// CompileCalicoPolicyMatcher compiles a policy's selectors once so it can be
// tested against many workloads.
func CompileCalicoPolicyMatcher(policy *unstructured.Unstructured) *CalicoPolicyMatcher {
	matcher := &CalicoPolicyMatcher{}
	if policy == nil {
		return matcher
	}
	if isStagedKubernetesNetworkPolicy(policy) {
		matcher.kubernetes = true
		selector, ok := calicoKubernetesPodSelector(policy)
		if !ok {
			return matcher
		}
		matcher.kubernetesSelector = selector
		matcher.endpointValid = true
		return matcher
	}

	selector, found, err := unstructured.NestedString(policy.Object, "spec", "selector")
	if err != nil {
		return matcher
	}
	if !found {
		selector = ""
	}
	endpoint, valid := compileCalicoSelector(selector)
	if !valid {
		return matcher
	}
	matcher.endpoint = endpoint
	matcher.endpointValid = true

	namespaceSelector, found, err := unstructured.NestedString(policy.Object, "spec", "namespaceSelector")
	switch {
	case err != nil:
		matcher.blocked = true
	case found && !isCalicoAllSelector(namespaceSelector):
		compiled, ok := compileCalicoSelector(namespaceSelector)
		if !ok {
			matcher.blocked = true
		} else {
			matcher.namespaceSelector = compiled
		}
	}

	serviceAccountSelector, found, err := unstructured.NestedString(policy.Object, "spec", "serviceAccountSelector")
	switch {
	case err != nil:
		matcher.blocked = true
	case found && !isCalicoAllSelector(serviceAccountSelector):
		compiled, ok := compileCalicoSelector(serviceAccountSelector)
		if !ok {
			matcher.blocked = true
		} else {
			matcher.serviceAccountSelector = compiled
		}
	}

	return matcher
}

// Matches reports whether the policy selects the workload, and whether its
// endpoint selector was usable at all. An unusable endpoint selector is the
// only case a caller must treat as "unknown" rather than "not selected".
func (m *CalicoPolicyMatcher) Matches(workloadLabels, namespaceLabels map[string]string, serviceAccountName string, serviceAccountLabels map[string]string) (matched, endpointSelectorValid bool) {
	if m == nil || !m.endpointValid {
		return false, false
	}
	if m.kubernetes {
		return m.kubernetesSelector.Matches(labels.Set(workloadLabels)), true
	}
	if !m.endpoint(workloadLabels) || m.blocked {
		return false, true
	}
	if m.namespaceSelector != nil && (namespaceLabels == nil || !m.namespaceSelector(namespaceLabels)) {
		return false, true
	}
	if m.serviceAccountSelector != nil {
		if serviceAccountName == "" || serviceAccountLabels == nil || !m.serviceAccountSelector(serviceAccountLabels) {
			return false, true
		}
	}
	return true, true
}

func CalicoPolicyMatchesWorkload(policy *unstructured.Unstructured, workloadLabels, namespaceLabels map[string]string, serviceAccountName string, serviceAccountLabels map[string]string) (matched, endpointSelectorValid bool) {
	return CompileCalicoPolicyMatcher(policy).Matches(workloadLabels, namespaceLabels, serviceAccountName, serviceAccountLabels)
}

func isStagedKubernetesNetworkPolicy(policy *unstructured.Unstructured) bool {
	return policy != nil && strings.EqualFold(policy.GetKind(), "StagedKubernetesNetworkPolicy")
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

// calicoPolicyMatchesAllWorkloads reports whether a policy selects every
// workload in its scope, which lets the builder record coverage without drawing
// an edge per workload. A namespace or service-account selector narrows the
// policy, so an all() endpoint selector alongside one covers a subset — the
// per-workload match has to decide, not this shortcut.
func calicoPolicyMatchesAllWorkloads(policy *unstructured.Unstructured, definition calicoPolicyDefinition) bool {
	if definition.kubernetes {
		selector, valid := calicoKubernetesPodSelector(policy)
		return valid && selector.String() == ""
	}
	for _, field := range []string{"namespaceSelector", "serviceAccountSelector"} {
		value, found, err := unstructured.NestedString(policy.Object, "spec", field)
		if err != nil || (found && !isCalicoAllSelector(value)) {
			return false
		}
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

				if definition.staged && !CalicoStagedActionPreviewsProtection(policy) {
					continue
				}

				matcher := CompileCalicoPolicyMatcher(policy)
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
