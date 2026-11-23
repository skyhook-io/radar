package vulnerabilities

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/skyhook-io/radar/pkg/audit"
)

// riskBits is a set of risk factors.
type riskBits uint8

const (
	bitPrivileged riskBits = 1 << iota
	bitHostAccess
	bitRunsAsRoot
	bitMayRunAsRoot
	bitCapabilities
	bitEscalation
	bitHostPort
)

// riskOrder is how factors list, as the screen's risk filter does: the
// serious ones first, then the common ones.
var riskOrder = []struct {
	factor RiskFactor
	bit    riskBits
	// label is the word the screen and the CSV use.
	label string
}{
	{RiskPrivileged, bitPrivileged, "Privileged"},
	{RiskHostAccess, bitHostAccess, "Host access"},
	{RiskRunsAsRoot, bitRunsAsRoot, "Runs as root"},
	{RiskMayRunAsRoot, bitMayRunAsRoot, "May run as root"},
	{RiskDangerousCapabilities, bitCapabilities, "Dangerous capabilities"},
	{RiskPrivilegeEscalation, bitEscalation, "Allows privilege escalation"},
	{RiskHostPort, bitHostPort, "Host port"},
}

func bitOf(f RiskFactor) riskBits {
	for _, r := range riskOrder {
		if r.factor == f {
			return r.bit
		}
	}
	return 0
}

// normalized drops may run as root where one runs as root: the stronger
// claim says it.
func (b riskBits) normalized() riskBits {
	if b&bitRunsAsRoot != 0 {
		return b &^ bitMayRunAsRoot
	}
	return b
}

func (b riskBits) factors() []RiskFactor {
	var out []RiskFactor
	for _, r := range riskOrder {
		if b.normalized()&r.bit != 0 {
			out = append(out, r.factor)
		}
	}
	return out
}

// riskCell is the risk_factors cell: the factors' words, joined.
func riskCell(factors []RiskFactor) string {
	words := make([]string, 0, len(factors))
	for _, f := range factors {
		for _, r := range riskOrder {
			if r.factor == f {
				words = append(words, r.label)
			}
		}
	}
	return joinCells(words)
}

// SeriousRisks are the factors the screen always marks: a workload that
// can reach the node or is root by its spec. The others are common (most
// images set no user, most specs leave escalation allowed) and are marked
// only while the risk filter selects them.
var SeriousRisks = []RiskFactor{RiskPrivileged, RiskHostAccess, RiskRunsAsRoot}

// shown are the factors the screen always marks.
var shown = riskMask(SeriousRisks)

// ParseRiskFactors reads the risk= filter: factors by name, comma-separated,
// any of which keeps a row. Empty keeps every row.
func ParseRiskFactors(s string) ([]RiskFactor, error) {
	var out []RiskFactor
	for _, part := range strings.Split(s, ",") {
		f := RiskFactor(strings.TrimSpace(part))
		switch {
		case f == "":
		case bitOf(f) != 0:
			out = append(out, f)
		default:
			names := make([]string, len(riskOrder))
			for i, r := range riskOrder {
				names[i] = string(r.factor)
			}
			return nil, fmt.Errorf("unknown risk factor %q: use %s", f, strings.Join(names, ", "))
		}
	}
	return out, nil
}

// riskMask is the filter's factors as a set; may run as root also takes
// runs as root, since a workload carrying that has dropped the weaker word.
func riskMask(factors []RiskFactor) riskBits {
	var m riskBits
	for _, f := range factors {
		m |= bitOf(f)
	}
	if m&bitMayRunAsRoot != 0 {
		m |= bitRunsAsRoot
	}
	return m
}

// workloadRisk is a workload's risk factors across its Pods, and the
// settings behind them, each once.
type workloadRisk struct {
	bits     riskBits
	settings []RiskSetting
}

func (w *workloadRisk) add(risks []audit.PodRisk) {
	for _, r := range risks {
		s := RiskSetting{Factor: RiskFactor(r.Factor), Check: r.CheckID, Message: r.Message}
		if slices.Contains(w.settings, s) {
			continue
		}
		w.bits |= bitOf(s.Factor)
		w.settings = append(w.settings, s)
	}
}

// risksOf gathers each workload's risk factors across its targets. A
// workload whose Pods all run as root says so, without may.
func risksOf(targets []target) map[WorkloadRef]*workloadRisk {
	out := map[WorkloadRef]*workloadRisk{}
	for i := range targets {
		t := &targets[i]
		if len(t.risks) == 0 {
			continue
		}
		w := out[t.workload]
		if w == nil {
			w = &workloadRisk{}
			out[t.workload] = w
		}
		w.add(t.risks)
	}
	for _, w := range out {
		if w.bits.normalized() != w.bits {
			w.bits = w.bits.normalized()
			w.settings = slices.DeleteFunc(w.settings, func(s RiskSetting) bool { return s.Factor == RiskMayRunAsRoot })
		}
		slices.SortFunc(w.settings, func(a, b RiskSetting) int {
			return cmp.Or(cmp.Compare(bitOf(a.Factor), bitOf(b.Factor)), cmp.Compare(a.Message, b.Message))
		})
	}
	return out
}

// riskOf is a workload's risk factors; a reference's container doesn't
// matter.
func (j *rollup) riskOf(w WorkloadRef) riskBits {
	w.Container = ""
	if r := j.risk[w]; r != nil {
		return r.bits
	}
	return 0
}

// risksAcross are the factors of the workloads in several lists together.
func (j *rollup) risksAcross(lists ...[]WorkloadRef) []RiskFactor {
	var b riskBits
	for _, list := range lists {
		for _, w := range list {
			b |= j.riskOf(w)
		}
	}
	return b.factors()
}

// imageRisks are the factors across a set of images' workloads, from each
// image's own (imageRisk).
func imageRisks(ids []string, imageRisk map[string]riskBits) []RiskFactor {
	var b riskBits
	for _, id := range ids {
		b |= imageRisk[id]
	}
	return b.factors()
}

// riskCounts counts the workloads in scope with each factor.
func riskCounts(risk map[WorkloadRef]*workloadRisk) map[RiskFactor]int {
	counts := map[RiskFactor]int{}
	for _, r := range riskOrder {
		counts[r.factor] = 0
	}
	for _, w := range risk {
		for _, f := range w.bits.factors() {
			counts[f]++
		}
	}
	return counts
}

// KeepNamedRisks lists in PodRisk the risky workloads the rows name, each
// once: in their workload lists, their exposed workloads, and By workload
// the rows themselves, with every factor but only the settings of those the
// screen marks (see PodRisk). A caller that trims the rows (MCP) calls it
// again.
func (r *Response) KeepNamedRisks() {
	if r.PodRisk == nil {
		return
	}
	r.PodRisk.Workloads = nil
	if len(r.risk) == 0 {
		return
	}
	seen := map[WorkloadRef]bool{}
	name := func(ws ...WorkloadRef) {
		for _, w := range ws {
			w.Container = ""
			if seen[w] {
				continue
			}
			seen[w] = true
			risk := r.risk[w]
			if risk == nil || risk.bits == 0 {
				continue
			}
			marked := shown | r.marked
			settings := slices.DeleteFunc(slices.Clone(risk.settings), func(s RiskSetting) bool { return bitOf(s.Factor)&marked == 0 })
			if settings == nil {
				settings = []RiskSetting{}
			}
			r.PodRisk.Workloads = append(r.PodRisk.Workloads, RiskyWorkload{WorkloadRef: w, Factors: risk.bits.factors(), Settings: settings})
		}
	}
	exposed := func(es []ExposedWorkload) {
		for _, e := range es {
			name(e.WorkloadRef)
		}
	}
	for _, img := range r.Images {
		name(img.Workloads...)
		exposed(img.Exposed)
	}
	for _, c := range r.Clean {
		name(c.Workloads...)
		exposed(c.Exposed)
	}
	for _, u := range r.Unscanned {
		name(u.Workloads...)
		exposed(u.Exposed)
	}
	for _, c := range r.CVEs {
		for _, a := range c.Affected {
			name(a.Workloads...)
		}
		exposed(c.Exposed)
	}
	for _, f := range r.Fixes {
		for _, a := range f.Affected {
			name(a.Workloads...)
		}
		exposed(f.Exposed)
	}
	for _, w := range r.Workloads {
		name(WorkloadRef{Kind: w.Kind, Group: w.Group, Namespace: w.Namespace, Name: w.Name})
	}
	name(r.CleanWorkloads...)
	slices.SortFunc(r.PodRisk.Workloads, func(a, b RiskyWorkload) int {
		return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Name, b.Name))
	})
}
