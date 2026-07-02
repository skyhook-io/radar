package diagnosecli

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"golang.org/x/term"
)

// renderer writes the live transcript + verdict to the terminal. In --json mode
// everything human goes to stderr so stdout stays a clean JSON document.
type renderer struct {
	w          *os.File
	color      bool
	inThinking bool
}

func newRenderer(jsonMode bool) *renderer {
	w := os.Stdout
	if jsonMode {
		w = os.Stderr
	}
	return &renderer{w: w, color: term.IsTerminal(int(w.Fd())) && os.Getenv("NO_COLOR") == ""}
}

const (
	cReset = "\x1b[0m"
	cDim   = "\x1b[2m"
	cBold  = "\x1b[1m"
	cGreen = "\x1b[32m"
	cRed   = "\x1b[31m"
	cAmber = "\x1b[33m"
	cCyan  = "\x1b[36m"
)

func (r *renderer) c(code, s string) string {
	if !r.color {
		return s
	}
	return code + s + cReset
}

func (r *renderer) header(run runSummary, base string) {
	target := run.Kind + " "
	if run.Namespace != "" {
		target += run.Namespace + "/"
	}
	target += run.Name
	fmt.Fprintf(r.w, "%s %s\n", r.c(cBold, "◉ Investigating"), target)
	fmt.Fprintf(r.w, "%s\n\n", r.c(cDim, fmt.Sprintf("run %s · via %s · watch: %s/?ai-run=%s", run.ID, agentDisplay(run.Agent), base, run.ID)))
}

func agentDisplay(name string) string {
	switch name {
	case "claude":
		return "Claude Code"
	case "codex":
		return "Codex"
	case "cursor-agent":
		return "Cursor"
	}
	return name
}

// thinking streams the agent's interleaved reasoning, dimmed.
func (r *renderer) thinking(token string) {
	if token == "" {
		return
	}
	if r.color {
		fmt.Fprint(r.w, cDim+token+cReset)
	} else {
		fmt.Fprint(r.w, token)
	}
	r.inThinking = !strings.HasSuffix(token, "\n")
}

func (r *renderer) breakThinking() {
	if r.inThinking {
		fmt.Fprintln(r.w)
		r.inThinking = false
	}
}

// step prints one completed tool call: "  ✓ get resource {"name":"web"} · 233ms".
func (r *renderer) step(s stepInfo) {
	r.breakThinking()
	line := "  " + r.c(cGreen, "✓") + " " + prettyTool(s.Tool)
	if s.Summary != "" {
		line += " " + r.c(cDim, compact(s.Summary, 80))
	}
	if s.Ms != nil {
		line += r.c(cDim, fmt.Sprintf(" · %dms", *s.Ms))
	}
	fmt.Fprintln(r.w, line)
}

func prettyTool(tool string) string {
	return strings.ReplaceAll(tool, "_", " ")
}

func compact(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max-1] + "…"
	}
	return s
}

func (r *renderer) errorLine(msg string) {
	r.breakThinking()
	fmt.Fprintf(r.w, "\n%s %s\n", r.c(cRed, "✗"), msg)
}

func (r *renderer) verdict(d diagnosis) {
	r.breakThinking()
	fmt.Fprintln(r.w)
	switch {
	case d.Healthy && d.RootCause == "":
		fmt.Fprintf(r.w, "%s\n", r.c(cGreen, r.c(cBold, "✔ No problems found")))
		if d.Report != "" {
			fmt.Fprintln(r.w, r.md(d.Report))
		}
	case d.Inconclusive && d.RootCause == "":
		fmt.Fprintf(r.w, "%s\n", r.c(cAmber, r.c(cBold, "? Couldn't determine")))
		if d.Report != "" {
			fmt.Fprintln(r.w, r.md(d.Report))
		}
	case d.RootCause != "":
		conf := ""
		if d.Confidence != nil {
			conf = r.c(cDim, " · confidence "+confidenceLabel(*d.Confidence))
		}
		fmt.Fprintf(r.w, "%s%s\n", r.c(cAmber, r.c(cBold, "▲ Root cause")), conf)
		fmt.Fprintln(r.w, r.md(d.RootCause))
		if len(d.Remediation) > 0 {
			fmt.Fprintf(r.w, "\n%s\n", r.c(cBold, "Remediation"))
			for i, step := range d.Remediation {
				marker := fmt.Sprintf("  %d.", i+1)
				if d.RecommendedIndex != nil && *d.RecommendedIndex == i+1 {
					marker = r.c(cGreen, "  ★"+fmt.Sprintf("%d.", i+1))
				}
				fmt.Fprintf(r.w, "%s %s\n", marker, r.md(step))
			}
			if d.RecommendedIndex != nil && d.RecommendedReason != "" {
				fmt.Fprintf(r.w, "  %s\n", r.c(cDim, "★ recommended: "+d.RecommendedReason))
			}
		}
	default:
		// No structured verdict — show whatever the agent said.
		if d.Report != "" {
			fmt.Fprintln(r.w, r.md(d.Report))
		} else {
			fmt.Fprintln(r.w, "The investigation finished without a clear result.")
		}
	}
	fmt.Fprintf(r.w, "\n%s\n", r.c(cDim, "AI-generated — review before applying. Continue in the Radar UI or your own agent."))
}

func confidenceLabel(c float64) string {
	switch {
	case c >= 0.8:
		return "high"
	case c >= 0.5:
		return "medium"
	}
	return "low"
}

var (
	mdBold   = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	mdInline = regexp.MustCompile("`([^`]+)`")
)

// md renders the verdict's GitHub-flavored markdown for a terminal: bold and
// inline code get ANSI treatment, everything else passes through.
func (r *renderer) md(s string) string {
	if !r.color {
		return s
	}
	s = mdBold.ReplaceAllString(s, cBold+"$1"+cReset)
	s = mdInline.ReplaceAllString(s, cCyan+"$1"+cReset)
	return s
}
