package context

import (
	"fmt"
	"strings"
)

// BudgetMode controls the token budget for context assembly.
type BudgetMode int

const (
	// BudgetLocal targets ~2-3K tokens for local LLMs (Ollama).
	BudgetLocal BudgetMode = iota
	// BudgetCloud targets ~4-8K tokens for cloud LLMs (OpenAI, Anthropic).
	BudgetCloud
)

// Approximate char-to-token ratio (conservative: ~4 chars per token for English text)
const charsPerToken = 4

func (b BudgetMode) maxChars() int {
	switch b {
	case BudgetLocal:
		return 3000 * charsPerToken // ~3K tokens
	case BudgetCloud:
		return 8000 * charsPerToken // ~8K tokens
	default:
		return 4000 * charsPerToken
	}
}

// ResourceVerbosity returns the VerbosityLevel to use for the primary resource
// in context assembly. Local budgets use Compact, cloud budgets use Detail.
func (b BudgetMode) ResourceVerbosity() VerbosityLevel {
	if b == BudgetLocal {
		return LevelCompact
	}
	return LevelDetail
}

// ContextSections holds all the pre-processed data sections for assembly.
type ContextSections struct {
	ResourceKind      string
	ResourceNamespace string
	ResourceName      string

	// Priority 1: Status and conditions (most diagnostic)
	MinifiedResource string

	// Priority 2: Events
	Events string

	// Priority 3: Logs
	Logs string

	// Priority 4: Metrics
	Metrics string

	// Priority 5: Relationships
	Relationships string

	// Priority 6: GitOps state (if applicable)
	GitOps string

	// Priority 7: Traffic info (if applicable)
	Traffic string

	// Context notes (absence indicators)
	Notes []string
}

// section represents a named content section with its priority for truncation.
type section struct {
	tag      string
	content  string
	priority int // lower = higher priority (kept first)
}

// AssembleContext builds a structured context string from pre-processed sections.
// Sections are wrapped in XML delimiters for prompt injection defense.
// Lower-priority sections are truncated if the budget is exceeded.
func AssembleContext(sections ContextSections, budget BudgetMode) string {
	var b strings.Builder

	// Resource identifier (always included, never truncated)
	fmt.Fprintf(&b, "Resource: %s/%s/%s\n\n", sections.ResourceKind, sections.ResourceNamespace, sections.ResourceName)

	// Build sections in priority order
	allSections := []section{
		{tag: "resource", content: sections.MinifiedResource, priority: 1},
		{tag: "events", content: sections.Events, priority: 2},
		{tag: "logs", content: sections.Logs, priority: 3},
		{tag: "metrics", content: sections.Metrics, priority: 4},
		{tag: "relationships", content: sections.Relationships, priority: 5},
		{tag: "gitops", content: sections.GitOps, priority: 6},
		{tag: "traffic", content: sections.Traffic, priority: 7},
	}

	// Context notes (absence indicators) — always include, very small
	if len(sections.Notes) > 0 {
		allSections = append(allSections, section{
			tag:      "context_notes",
			content:  strings.Join(sections.Notes, "\n"),
			priority: 0, // highest priority
		})
	}

	// Filter out empty sections
	var activeSections []section
	for _, s := range allSections {
		if strings.TrimSpace(s.content) != "" {
			activeSections = append(activeSections, s)
		}
	}

	maxChars := budget.maxChars()
	remaining := maxChars - b.Len()

	// Add sections in priority order, truncating if needed
	// First pass: calculate total size
	totalSize := 0
	for _, s := range activeSections {
		totalSize += len(s.content) + len(s.tag)*2 + 10 // account for XML tags + newlines
	}

	if totalSize <= remaining {
		// Everything fits — write all sections
		for _, s := range activeSections {
			writeSection(&b, s.tag, s.content)
		}
	} else {
		// Need to truncate — write in priority order, truncate lower priorities
		for _, s := range activeSections {
			sectionSize := len(s.content) + len(s.tag)*2 + 10
			if sectionSize <= remaining {
				writeSection(&b, s.tag, s.content)
				remaining -= sectionSize
			} else if remaining > 100 {
				// Truncate this section to fit remaining budget
				availableContent := remaining - len(s.tag)*2 - 10
				if availableContent > 50 {
					truncated := s.content[:availableContent] + "\n... (truncated)"
					writeSection(&b, s.tag, truncated)
				}
				break // no more room
			}
		}
	}

	return b.String()
}

func writeSection(b *strings.Builder, tag, content string) {
	fmt.Fprintf(b, "<%s>\n%s\n</%s>\n\n", tag, strings.TrimSpace(content), tag)
}
