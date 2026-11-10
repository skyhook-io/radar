package ownership_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

var updateGolden = flag.Bool("update-ownership-golden", false, "rewrite the ownership view golden")

// The golden records every answer a View gives about every object of the
// ownership fixture, for a caller who may read everything and for one who
// may read only alpha and argocd. It was checked by hand when written; a
// change that alters an answer regenerates it with -update-ownership-golden
// and justifies each changed line.
func TestViewGolden(t *testing.T) {
	w := ownershiptest.Fixture()
	refs := make([]resourceid.Ref, 0, len(w.Objects))
	for _, o := range w.Objects {
		refs = append(refs, o.Ref)
	}
	slices.SortFunc(refs, func(a, b resourceid.Ref) int { return compareRef(a, b) })
	record := map[string][]goldenAnswer{}
	for name, caller := range map[string]ownership.Access{
		"everything":       ownership.Unrestricted(),
		"alpha and argocd": access{namespaces: []string{"alpha", "argocd"}},
	} {
		v := view(t, w, caller)
		for _, ref := range refs {
			record[name] = append(record[name], answer(v, ref))
		}
	}
	got, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "view-fixture.json")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update-ownership-golden to create it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("view answers differ from %s; rerun with -update-ownership-golden and justify each changed line", path)
	}
}

func compareRef(a, b resourceid.Ref) int {
	for _, c := range [][2]string{{a.Group, b.Group}, {a.Kind, b.Kind}, {a.Namespace, b.Namespace}, {a.Name, b.Name}} {
		if c[0] != c[1] {
			if c[0] < c[1] {
				return -1
			}
			return 1
		}
	}
	return 0
}

type goldenAnswer struct {
	Object     string   `json:"object"`
	Subject    string   `json:"subject"`
	References []string `json:"references,omitempty"`
	Owner      string   `json:"owner,omitempty"`
	Children   []string `json:"children,omitempty"`
	Coverage   []string `json:"childrenCoverage,omitempty"`
	Chain      string   `json:"controllerChain,omitempty"`
	Members    []string `json:"members,omitempty"`
	Membership string   `json:"membership,omitempty"`
}

func resolutionText(r ownership.OwnerResolution) string {
	s := r.State.String()
	if r.Ref != (resourceid.Ref{}) {
		s += " " + r.Ref.String()
	}
	if r.Gap.Reason != "" {
		s += fmt.Sprintf(" (%s %s/%s)", r.Gap.Reason, r.Gap.Kind, r.Gap.Namespace)
	}
	return s
}

func answer(v *ownership.View, ref resourceid.Ref) goldenAnswer {
	a := goldenAnswer{Object: ref.String()}
	subject, refs := v.References(ref)
	a.Subject = subject.Access.String()
	if subject.Access == ownership.Allowed {
		a.Subject = subject.Read.State.String()
		if subject.Read.State == ownership.Unavailable {
			a.Subject += " " + string(subject.Read.Gap.Reason)
		}
	}
	if !subject.Present() {
		return a
	}
	for _, r := range refs {
		a.References = append(a.References, resolutionText(r))
	}
	switch owner := v.Owner(ref); {
	case owner.Several:
		a.Owner = "several, none a controller"
	case owner.Reference != nil:
		a.Owner = fmt.Sprintf("reference %d: %s", referenceIndex(owner), resolutionText(owner.Resolution))
	}
	children := v.Children(ref)
	for _, c := range children.Children {
		s := c.Ref.String()
		if slices.ContainsFunc(c.OwnerReferences, func(r metav1.OwnerReference) bool {
			return r.UID == subject.Read.Object.UID && isControllerRef(r)
		}) {
			s += " (controller)"
		}
		a.Children = append(a.Children, s)
	}
	for _, g := range children.Gaps {
		a.Coverage = append(a.Coverage, fmt.Sprintf("not read: %s %s/%s", g.Reason, g.Kind, g.Namespace))
	}
	if children.Narrowed {
		a.Coverage = append(a.Coverage, "narrowed to what the caller may read")
	}
	chain := v.ControllerChain(ref)
	end := chain.Chain[len(chain.Chain)-1]
	switch {
	case chain.Cycle:
		a.Chain = "cycle after " + end.String()
	case chain.Truncated:
		a.Chain = "truncated at " + end.String()
	case chain.UnfollowedReference != nil:
		a.Chain = fmt.Sprintf("%s, stopped by %s", end, resolutionText(*chain.UnfollowedReference))
	case end != ref:
		a.Chain = end.String()
	}
	if ownership.IsRecordingManager(ref.GroupKind()) {
		m := v.Members(ref)
		a.Membership = fmt.Sprintf("%s; %d missing, %d withheld, %d unknown", m.Membership, m.Missing, m.Withheld, m.Unknown)
		for _, o := range m.Members {
			a.Members = append(a.Members, o.Ref.String())
		}
		for _, g := range m.Gaps {
			a.Membership += fmt.Sprintf("; not read: %s %s/%s", g.Reason, g.Kind, g.Namespace)
		}
	}
	return a
}
