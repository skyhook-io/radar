package netpol

import (
	"regexp"
	"strings"
	"unicode"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

// CalicoEndpointLabels returns the labels Calico exposes for a Kubernetes
// endpoint, including the labels it adds automatically to every workload. An
// empty serviceAccount is the namespace's default one, as on a pod spec.
func CalicoEndpointLabels(namespace string, workloadLabels map[string]string, serviceAccount string) map[string]string {
	endpointLabels := make(map[string]string, len(workloadLabels)+3)
	for key, value := range workloadLabels {
		endpointLabels[key] = value
	}
	endpointLabels["projectcalico.org/namespace"] = namespace
	endpointLabels["projectcalico.org/orchestrator"] = "k8s"
	if serviceAccount == "" {
		serviceAccount = "default"
	}
	// Calico sets this only when the name fits in a label value.
	if len(serviceAccount) < 63 {
		endpointLabels["projectcalico.org/serviceaccount"] = serviceAccount
	}
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

// IsCalicoAllSelector reports whether a Calico selector selects everything.
func IsCalicoAllSelector(expression string) bool {
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

// CalicoServiceAccountLabels are the labels a Calico serviceAccountSelector
// matches: the ServiceAccount's own and the two naming it.
func CalicoServiceAccountLabels(serviceAccount *corev1.ServiceAccount) map[string]string {
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
		selector, ok := CalicoKubernetesPodSelector(policy)
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
	case found && !IsCalicoAllSelector(namespaceSelector):
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
	case found && !IsCalicoAllSelector(serviceAccountSelector):
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

// CalicoPolicyMatchesWorkload compiles policy and matches one workload.
func CalicoPolicyMatchesWorkload(policy *unstructured.Unstructured, workloadLabels, namespaceLabels map[string]string, serviceAccountName string, serviceAccountLabels map[string]string) (matched, endpointSelectorValid bool) {
	return CompileCalicoPolicyMatcher(policy).Matches(workloadLabels, namespaceLabels, serviceAccountName, serviceAccountLabels)
}

func isStagedKubernetesNetworkPolicy(policy *unstructured.Unstructured) bool {
	return policy != nil && strings.EqualFold(policy.GetKind(), "StagedKubernetesNetworkPolicy")
}

// CalicoKubernetesPodSelector reads a StagedKubernetesNetworkPolicy's
// podSelector: an absent or empty one selects every pod.
func CalicoKubernetesPodSelector(policy *unstructured.Unstructured) (labels.Selector, bool) {
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

// CalicoNamespaceLabels are the labels a Calico namespaceSelector matches:
// the Namespace's own and the two naming it.
func CalicoNamespaceLabels(namespace *corev1.Namespace) map[string]string {
	labels := make(map[string]string, len(namespace.Labels)+2)
	for key, value := range namespace.Labels {
		labels[key] = value
	}
	labels["kubernetes.io/metadata.name"] = namespace.Name
	labels["projectcalico.org/name"] = namespace.Name
	return labels
}

// Needs reports which labels beyond a workload's own Matches reads: the
// Namespace's for a namespaceSelector, the ServiceAccount's for a
// serviceAccountSelector. raw is true for a StagedKubernetesNetworkPolicy,
// which matches the pod's labels as they are, without Calico's endpoint
// labels.
func (m *CalicoPolicyMatcher) Needs() (namespace, serviceAccount, raw bool) {
	if m == nil {
		return false, false, false
	}
	return m.namespaceSelector != nil, m.serviceAccountSelector != nil, m.kubernetes
}
