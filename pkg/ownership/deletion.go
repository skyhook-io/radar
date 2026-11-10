package ownership

import (
	"context"
	"slices"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

// DeletionAnswer projects garbage collection after the subject is removed.
// It is an observation, not a server dry-run or a guarantee that controllers
// will finish. Finalizers, admission and controllers may delay or change it.
type DeletionAnswer struct {
	Subject              Subject
	Dependents, Possible []Observation
	Gaps                 []Gap
	Narrowed, Truncated  bool
}

// Deletion follows every owner reference, including non-controller references.
// A live owner outside the deletion prevents collection; an unknown owner makes
// collection possible. Cycles without a proven path to collection stay possible.
// limit bounds the objects retained and expanded, including survivors. ValidateCaches must
// still pass before the answer is used.
func (v *View) Deletion(ctx context.Context, ref resourceid.Ref, limit int) DeletionAnswer {
	a := DeletionAnswer{Subject: v.subject(ref)}
	if !a.Subject.Present() {
		return a
	}
	objects := map[resourceid.Ref]Observation{ref: a.Subject.Read.Object}
	order := []resourceid.Ref{ref}
	for i := 0; i < len(order); i++ {
		if ctx.Err() != nil {
			a.Truncated = true
			break
		}
		current := order[i]
		children := v.Children(current)
		a.Gaps = append(a.Gaps, children.Gaps...)
		a.Narrowed = a.Narrowed || children.Narrowed
		candidates := children.Children
		if found, searched := v.searches[current]; searched {
			readable, _, _ := v.candidates(found)
			for _, c := range readable {
				for _, owner := range c.OwnerReferences {
					if owner.UID != objects[current].UID {
						continue
					}
					resolution := v.resolve(c, owner)
					if resolution.State == OwnerUnknown && mayResolveTo(resolution, owner.Name, current) {
						candidates = append(candidates, c)
						break
					}
				}
			}
		}
		for _, child := range candidates {
			if _, seen := objects[child.Ref]; seen {
				continue
			}
			if len(objects) >= limit {
				a.Truncated = true
				continue
			}
			objects[child.Ref] = child
			order = append(order, child.Ref)
		}
	}
	const (
		survives = iota
		possible
		collected
	)
	fates := map[resourceid.Ref]int{ref: collected}
	ownersByRef := make(map[resourceid.Ref][]OwnerResolution, len(objects))
	for _, r := range order {
		if r != ref {
			fates[r] = possible
			if ctx.Err() == nil {
				_, ownersByRef[r] = v.References(r)
			}
		}
	}
	// Propagate certainty in either direction until shared-owner paths settle.
	for changed := true; changed; {
		changed = false
		for _, r := range order {
			if r == ref {
				continue
			}
			if ctx.Err() != nil {
				a.Truncated = true
				break
			}
			fate := collected
			for _, owner := range ownersByRef[r] {
				switch owner.State {
				case OwnerFound:
					switch fates[owner.Ref] {
					case survives:
						if _, visited := objects[owner.Ref]; !visited && a.Truncated {
							if fate != survives {
								fate = possible
							}
						} else {
							fate = survives
						}
					case possible:
						if fate != survives {
							fate = possible
						}
					}
				case OwnerGone, OwnerReplaced:
				case OwnerUnresolvable:
					fate = survives
				default:
					if fate != survives {
						fate = possible
					}
				}
			}
			if fates[r] != fate {
				fates[r] = fate
				changed = true
			}
		}
		if ctx.Err() != nil {
			break
		}
	}
	if ctx.Err() != nil {
		for r := range objects {
			if r != ref && fates[r] == collected {
				fates[r] = possible
			}
		}
	}
	for r, o := range objects {
		if r == ref {
			continue
		}
		switch fates[r] {
		case collected:
			a.Dependents = append(a.Dependents, o)
		case possible:
			a.Possible = append(a.Possible, o)
		}
	}
	sort := func(a, b Observation) int { return compareRefs(a.Ref, b.Ref) }
	slices.SortFunc(a.Dependents, sort)
	slices.SortFunc(a.Possible, sort)
	slices.SortFunc(a.Gaps, compareGaps)
	a.Gaps = slices.Compact(a.Gaps)
	return a
}
