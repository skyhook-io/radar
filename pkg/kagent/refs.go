package kagent

import (
	"slices"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

// The kinds an Agent's references name besides kagent's own. Radar reads none
// of them for the workspace: kagent's ModelConfig and RemoteMCPServer
// conditions report a missing Secret or ConfigMap, and the WorkerPool is
// Substrate's.
const (
	KindSecret     = "Secret"
	KindConfigMap  = "ConfigMap"
	KindWorkerPool = "WorkerPool"
	// WorkerPoolGroup is Substrate's API group.
	WorkerPoolGroup = "ate.dev"
)

// ObjectRef names an object, or a coordinate a reference declares.
type ObjectRef struct {
	Group     string `json:"group"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// Key is kind/namespace/name.
func (r ObjectRef) Key() string { return r.Kind + "/" + r.Namespace + "/" + r.Name }

// NamespacedName is namespace/name, as kagent quotes it.
func (r ObjectRef) NamespacedName() string { return r.Namespace + "/" + r.Name }

func kagentRef(kind, namespace, name string) ObjectRef {
	return ObjectRef{Group: Group, Kind: kind, Namespace: namespace, Name: name}
}

func refOf(u *unstructured.Unstructured) ObjectRef {
	return kagentRef(u.GetKind(), u.GetNamespace(), u.GetName())
}

// What Radar can say about one declared reference (Reference.State).
const (
	RefPresent = "present"
	// RefAbsent: the target's kind was read in full in its namespace and it is
	// not there.
	RefAbsent = "absent"
	// RefUnread: the target's kind was not read in its namespace for this
	// caller; Reference.Unread says how the read went.
	RefUnread = "unread"
	// RefNotPermitted: the target exists, and its allowedNamespaces refuses
	// references from the declaring object's namespace.
	RefNotPermitted = "notPermitted"
	// RefDeclared: a Secret, ConfigMap or WorkerPool, which Radar does not
	// read here.
	RefDeclared = "declared"
)

// How a reference is used (Reference.Role).
const (
	RoleTemplate          = "template"
	RoleSubagentTemplate  = "subagentTemplate"
	RoleHarness           = "harness"
	RoleModel             = "model"
	RoleSubagentModel     = "subagentModel"
	RoleSummarizer        = "summarizer"
	RoleEmbedding         = "embedding"
	RoleMCPServer         = "mcpServer"
	RoleSubagentMCPServer = "subagentMcpServer"
	RoleEgressPolicy      = "egressPolicy"
	RoleWorkerPool        = "workerPool"
	RoleSystemPrompt      = "systemPrompt"
	RolePromptSource      = "promptSource"
	RoleOutputSchema      = "outputSchema"
	RoleAPIKey            = "apiKey"
	RoleEndpoint          = "endpoint"
	RoleHeader            = "header"
)

// Reference is one reference an object declares, resolved with kagent's
// namespace defaulting.
type Reference struct {
	Role   string    `json:"role"`
	Target ObjectRef `json:"target"`
	// From is the object that declares it, and Field the field it does so in.
	From  ObjectRef `json:"from"`
	Field string    `json:"field"`
	State string    `json:"state"`
	// Unread is the coverage state or read reason for an unread target.
	Unread  string       `json:"unread,omitempty"`
	Reading *ReadingView `json:"reading,omitempty"`
	// PolicyUnknown: the target's allowedNamespaces selects namespaces by
	// label and the declaring namespace's labels were not read, so whether
	// kagent permits the reference is unknown.
	PolicyUnknown bool `json:"policyUnknown,omitempty"`
	// Tools are the tools an MCP binding asks for; empty asks for all.
	Tools []string `json:"tools,omitempty"`
	// Key is the Secret or ConfigMap key a reference names.
	Key string `json:"key,omitempty"`
	// Fallback: the Harness names no summarizer, so the runtime summarizes
	// with the Agent's own model.
	Fallback bool `json:"fallback,omitempty"`
}

// Objects is what one kind's read returned for the caller.
type Objects struct {
	Items []*unstructured.Unstructured
	// Read reports whether every object of the kind in a namespace was read:
	// only there is an object missing from Items absent.
	Read func(namespace string) bool
	// UnreadReason explains a namespace outside the scope State describes.
	UnreadReason func(namespace string) string
	// Complete: every namespace of the request scope was read.
	Complete bool
	// State is the read's coverage state, Radar's KindCoverage vocabulary
	// (full, partial, denied, notInstalled, syncing, uncached, error); a
	// reference left unread quotes it unless UnreadReason refines it.
	State string
}

// Inventory is one caller's read of kagent's kinds, by Kind*. A kind missing
// from Kinds was not read at all.
type Inventory struct {
	Now   time.Time
	Kinds map[string]Objects
	// NamespaceLabels returns a namespace's labels, for an allowedNamespaces
	// policy that selects by label; ok is false when they were not read.
	NamespaceLabels func(namespace string) (labels map[string]string, ok bool)
}

// Kinds is every kagent kind the workspace reads.
var Kinds = []string{KindAgent, KindAgentTemplate, KindHarness, KindModelConfig, KindRemoteMCPServer, KindEgressPolicy, KindModelProviderConfig, KindSandboxTemplate}

// An Agent's template tree is walked through at most MaxTemplates
// AgentTemplates and MaxDepth subagent levels; beyond them its references are
// not listed and its impact is a lower bound. kagent itself rejects a subagent
// that has subagents of its own, so a valid tree is two levels deep.
const (
	MaxTemplates = 32
	MaxDepth     = 4
)

type index struct {
	inv     Inventory
	objects map[string]map[string]*unstructured.Unstructured
}

func newIndex(inv Inventory) *index {
	ix := &index{inv: inv, objects: map[string]map[string]*unstructured.Unstructured{}}
	for kind, objs := range inv.Kinds {
		byName := map[string]*unstructured.Unstructured{}
		for _, u := range objs.Items {
			if u == nil || groupOf(u.GetAPIVersion()) != Group || u.GetKind() != kind {
				continue
			}
			byName[u.GetNamespace()+"/"+u.GetName()] = u
		}
		ix.objects[kind] = byName
	}
	return ix
}

func (ix *index) items(kind string) []*unstructured.Unstructured {
	byName := ix.objects[kind]
	out := make([]*unstructured.Unstructured, 0, len(byName))
	for _, u := range byName {
		out = append(out, u)
	}
	slices.SortFunc(out, func(a, b *unstructured.Unstructured) int {
		return strings.Compare(a.GetNamespace()+"/"+a.GetName(), b.GetNamespace()+"/"+b.GetName())
	})
	return out
}

// read reports whether every object of kind in namespace was read.
func (ix *index) read(kind, namespace string) bool {
	objs, ok := ix.inv.Kinds[kind]
	return ok && objs.Read != nil && objs.Read(namespace)
}

func (ix *index) complete(kind string) bool {
	objs, ok := ix.inv.Kinds[kind]
	return ok && objs.Complete
}

// resolve looks a kagent target up: the object, or why there is none.
func (ix *index) resolve(target ObjectRef) (u *unstructured.Unstructured, state, unread string) {
	if u := ix.objects[target.Kind][target.Namespace+"/"+target.Name]; u != nil {
		return u, RefPresent, ""
	}
	if ix.read(target.Kind, target.Namespace) {
		return nil, RefAbsent, ""
	}
	why := "notRead"
	if objs, ok := ix.inv.Kinds[target.Kind]; ok {
		if objs.UnreadReason != nil {
			why = objs.UnreadReason(target.Namespace)
		} else if objs.State != "" && objs.State != "full" {
			why = objs.State
		}
	}
	return nil, RefUnread, why
}

// permitted evaluates a ModelConfig's or RemoteMCPServer's allowedNamespaces
// for a reference from source, as kagent's AllowsNamespaceMetadata does
// (api/v1alpha3/common_types.go): no policy, or Same, permits only the
// target's own namespace; a Selector needs the source namespace's labels,
// even when the namespaces are equal. known is false when they weren't read.
func (ix *index) permitted(target *unstructured.Unstructured, source string) (ok, known bool) {
	policy, found, _ := unstructured.NestedMap(target.Object, "spec", "allowedNamespaces")
	if !found {
		return source == target.GetNamespace(), true
	}
	from, _ := policy["from"].(string)
	switch from {
	case "All":
		return true, true
	case "Same", "":
		return source == target.GetNamespace(), true
	case "Selector":
		raw, _ := policy["selector"].(map[string]any)
		if raw == nil {
			return false, true
		}
		var sel metav1.LabelSelector
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &sel); err != nil {
			return false, true
		}
		selector, err := metav1.LabelSelectorAsSelector(&sel)
		if err != nil {
			return false, true
		}
		if ix.inv.NamespaceLabels == nil {
			return false, false
		}
		nsLabels, read := ix.inv.NamespaceLabels(source)
		if !read {
			return false, false
		}
		return selector.Matches(labels.Set(nsLabels)), true
	}
	return false, true
}

// agentWalk is everything one Agent's spec declares, transitively.
type agentWalk struct {
	refs []Reference
	// incomplete: an intermediate object was unread, or the tree was cut, so
	// what the Agent uses beyond it is not known.
	incomplete bool
	truncated  bool
	// cycle names the AgentTemplates of a subagent cycle, as kagent's compiler
	// reports one.
	cycle []string
	// inline: the Agent defines its template inline.
	inline      bool
	harnessType string
	// rootModel is the root template's ModelConfig, when it names one.
	rootModel *ObjectRef
}

type templateNode struct {
	ref    ObjectRef
	spec   map[string]any
	inline bool
	// field prefixes the template spec's fields in the declaring object.
	field string
}

// walkAgent resolves an Agent's references as kagent's compiler does
// (core/internal/translator/compiler.go): the template inline or by name in
// the Agent's namespace, the Harness in the Agent's namespace, then the
// template tree, whose models and MCP servers default to the template's
// namespace and whose subagent templates are always in it.
func (ix *index) walkAgent(a *unstructured.Unstructured) agentWalk {
	w := agentWalk{}
	agent := refOf(a)
	ns := a.GetNamespace()

	var root *templateNode
	if inline, found, _ := unstructured.NestedMap(a.Object, "spec", "template", "inline"); found {
		w.inline = true
		root = &templateNode{ref: agent, spec: inline, inline: true, field: "spec.template.inline."}
	} else if name, _, _ := unstructured.NestedString(a.Object, "spec", "template", "ref", "name"); name != "" {
		target := kagentRef(KindAgentTemplate, ns, name)
		u, state, unread := ix.resolve(target)
		w.add(Reference{Role: RoleTemplate, Target: target, From: agent, Field: "spec.template.ref", State: state, Unread: unread})
		switch state {
		case RefPresent:
			spec, _, _ := unstructured.NestedMap(u.Object, "spec")
			root = &templateNode{ref: target, spec: spec, field: "spec."}
		case RefUnread:
			w.incomplete = true
		}
	}

	var harness *unstructured.Unstructured
	if name, _, _ := unstructured.NestedString(a.Object, "spec", "harnessRef", "name"); name != "" {
		target := kagentRef(KindHarness, ns, name)
		u, state, unread := ix.resolve(target)
		w.add(Reference{Role: RoleHarness, Target: target, From: agent, Field: "spec.harnessRef", State: state, Unread: unread})
		if state == RefPresent {
			harness = u
		} else if state == RefUnread {
			w.incomplete = true
		}
	}

	refs, _, _ := unstructured.NestedSlice(a.Object, "spec", "egressPolicyRefs")
	for i, raw := range refs {
		m, _ := raw.(map[string]any)
		if name, _ := m["name"].(string); name != "" {
			target := kagentRef(KindEgressPolicy, ns, name)
			_, state, unread := ix.resolve(target)
			w.add(Reference{Role: RoleEgressPolicy, Target: target, From: agent, Field: "spec.egressPolicyRefs[" + strconv.Itoa(i) + "]", State: state, Unread: unread})
		}
	}

	if root != nil {
		t := treeWalk{ix: ix, w: &w, seen: map[string]bool{}, path: map[string]bool{}}
		t.visit(*root, 0)
	}
	if harness != nil {
		ix.walkHarness(&w, harness)
	}
	return w
}

func (w *agentWalk) add(r Reference) { w.refs = append(w.refs, r) }

type treeWalk struct {
	ix      *index
	w       *agentWalk
	seen    map[string]bool
	path    map[string]bool
	visited int
}

func (t *treeWalk) visit(node templateNode, depth int) {
	ns := node.ref.Namespace
	// Inline roots are not references and can't be part of a cycle.
	if !node.inline {
		t.seen[node.ref.Name], t.path[node.ref.Name] = true, true
		defer delete(t.path, node.ref.Name)
	}
	modelRole, mcpRole := RoleModel, RoleMCPServer
	if depth > 0 {
		modelRole, mcpRole = RoleSubagentModel, RoleSubagentMCPServer
	}
	if name, namespace, ok := objectReference(node.spec, "modelConfig"); ok {
		target := kagentRef(KindModelConfig, defaultNS(namespace, ns), name)
		ref := t.ix.dependency(Reference{Role: modelRole, Target: target, From: node.ref, Field: node.field + "modelConfig"}, ns)
		t.w.add(ref)
		if depth == 0 {
			t.w.rootModel = &target
		}
	}
	for _, source := range [...]struct{ field, role string }{{"systemPromptFrom", RoleSystemPrompt}, {"outputSchemaFrom", RoleOutputSchema}} {
		field, role := source.field, source.role
		if m, ok := node.spec[field].(map[string]any); ok {
			if name, _ := m["name"].(string); name != "" {
				key, _ := m["key"].(string)
				t.w.add(Reference{Role: role, Target: ObjectRef{Kind: KindConfigMap, Namespace: ns, Name: name}, From: node.ref, Field: node.field + field, State: RefDeclared, Key: key})
			}
		}
	}
	sources, _, _ := unstructured.NestedSlice(node.spec, "promptTemplate", "dataSources")
	for i, raw := range sources {
		m, _ := raw.(map[string]any)
		if name, _ := m["name"].(string); name != "" {
			t.w.add(Reference{Role: RolePromptSource, Target: ObjectRef{Kind: KindConfigMap, Namespace: ns, Name: name}, From: node.ref,
				Field: node.field + "promptTemplate.dataSources[" + strconv.Itoa(i) + "]", State: RefDeclared})
		}
	}
	tools, _ := node.spec["tools"].([]any)
	var children []templateNode
	for i, raw := range tools {
		tool, _ := raw.(map[string]any)
		field := node.field + "tools[" + strconv.Itoa(i) + "]"
		if mcp, ok := tool["mcp"].(map[string]any); ok {
			server, _ := mcp["server"].(map[string]any)
			name, _ := server["name"].(string)
			kind, _ := server["kind"].(string)
			if name == "" || kind != KindRemoteMCPServer {
				continue
			}
			namespace, _ := server["namespace"].(string)
			ref := t.ix.dependency(Reference{Role: mcpRole, Target: kagentRef(KindRemoteMCPServer, defaultNS(namespace, ns), name), From: node.ref, Field: field + ".mcp.server", Tools: stringSlice(mcp["tools"])}, ns)
			t.w.add(ref)
			continue
		}
		sub, ok := tool["subAgent"].(map[string]any)
		if !ok {
			continue
		}
		templateRef, _ := sub["templateRef"].(map[string]any)
		name, _ := templateRef["name"].(string)
		if name == "" {
			continue
		}
		target := kagentRef(KindAgentTemplate, ns, name)
		if t.path[name] {
			t.w.cycle = append(t.w.cycle, name)
			continue
		}
		if t.seen[name] {
			continue
		}
		u, state, unread := t.ix.resolve(target)
		t.w.add(Reference{Role: RoleSubagentTemplate, Target: target, From: node.ref, Field: field + ".subAgent.templateRef", State: state, Unread: unread})
		switch state {
		case RefUnread:
			t.w.incomplete = true
		case RefPresent:
			spec, _, _ := unstructured.NestedMap(u.Object, "spec")
			children = append(children, templateNode{ref: target, spec: spec, field: "spec."})
		}
	}
	for _, child := range children {
		if t.seen[child.ref.Name] {
			continue
		}
		if depth+1 > MaxDepth || t.visited >= MaxTemplates {
			t.w.truncated, t.w.incomplete = true, true
			continue
		}
		t.visited++
		t.visit(child, depth+1)
	}
}

// dependency resolves a ModelConfig or RemoteMCPServer reference, layering
// its sharing policy for the declaring namespace onto a present target.
func (ix *index) dependency(r Reference, source string) Reference {
	u, state, unread := ix.resolve(r.Target)
	r.State, r.Unread = state, unread
	if u == nil {
		return r
	}
	switch ok, known := ix.permitted(u, source); {
	case !known:
		r.PolicyUnknown = true
	case !ok:
		r.State = RefNotPermitted
	}
	return r
}

// walkHarness adds a Harness's references: its WorkerPool, and for the kagent
// runtime the summarizer and memory models, which default to the Harness's
// namespace. A summarizer that is the Agent's own model, or none, is the
// Agent's model summarizing (core/internal/translator/adkconfig/runtime.go).
func (ix *index) walkHarness(w *agentWalk, h *unstructured.Unstructured) {
	from := refOf(h)
	ns := h.GetNamespace()
	spec, _, _ := unstructured.NestedMap(h.Object, "spec")
	for _, t := range []string{"kagent", "codex", "claude", "byo"} {
		if _, ok := spec[t]; ok {
			w.harnessType = t
			break
		}
	}
	if pool, _, _ := unstructured.NestedString(spec, "substrate", "workerPoolRef", "name"); pool != "" {
		w.add(Reference{Role: RoleWorkerPool, Target: ObjectRef{Group: WorkerPoolGroup, Kind: KindWorkerPool, Namespace: ns, Name: pool}, From: from, Field: "spec.substrate.workerPoolRef", State: RefDeclared})
	}
	if w.harnessType != "kagent" {
		return
	}
	if _, found, _ := unstructured.NestedMap(spec, "kagent", "compaction"); found {
		name, namespace, named := objectReference(nestedMap(spec, "kagent", "compaction", "summarizer"), "modelConfigRef")
		target := kagentRef(KindModelConfig, defaultNS(namespace, ns), name)
		switch {
		case named && (w.rootModel == nil || *w.rootModel != target):
			w.add(ix.dependency(Reference{Role: RoleSummarizer, Target: target, From: from, Field: "spec.kagent.compaction.summarizer.modelConfigRef"}, ns))
		case w.rootModel != nil:
			r := Reference{Role: RoleSummarizer, Target: *w.rootModel, From: from, Field: "spec.kagent.compaction", Fallback: true}
			for _, existing := range w.refs {
				if existing.Role == RoleModel && existing.Target == *w.rootModel {
					r.State, r.Unread, r.PolicyUnknown = existing.State, existing.Unread, existing.PolicyUnknown
				}
			}
			w.add(r)
		}
	}
	if name, namespace, ok := objectReference(nestedMap(spec, "kagent", "memory"), "modelConfigRef"); ok {
		w.add(ix.dependency(Reference{Role: RoleEmbedding, Target: kagentRef(KindModelConfig, defaultNS(namespace, ns), name), From: from, Field: "spec.kagent.memory.modelConfigRef"}, ns))
	}
}

// objectReference reads an ObjectReference field ({name, namespace}).
func objectReference(m map[string]any, field string) (name, namespace string, ok bool) {
	ref, _ := m[field].(map[string]any)
	name, _ = ref["name"].(string)
	namespace, _ = ref["namespace"].(string)
	return name, namespace, name != ""
}

func nestedMap(m map[string]any, fields ...string) map[string]any {
	out, _, _ := unstructured.NestedMap(m, fields...)
	return out
}

func defaultNS(namespace, owner string) string {
	if namespace != "" {
		return namespace
	}
	return owner
}

func stringSlice(v any) []string {
	raw, _ := v.([]any)
	var out []string
	for _, s := range raw {
		if str, ok := s.(string); ok && str != "" {
			out = append(out, str)
		}
	}
	return out
}
