package ai

import (
	"encoding/json"
	"github.com/skyhook-io/radar/pkg/resourceapi"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/investigationrefs"
	"github.com/skyhook-io/radar/pkg/investigation"
)

// partialListFixture is shared with internal/mcp, which pins it to the
// producer, and with web's investigationEvidence projection, which reads the
// joined text the way Findings does.
const partialListFixture = "../../testdata/mcp/list-resources-partial-coverage.json"

// A partial list_resources read carries its rows and their coverage in one
// text block. Every agent backend must hand Findings exactly that text, and
// the private evidence ledger must still recognize it, so the coverage reaches
// the investigation together with the rows. A second text block would be
// joined without a separator (or, by OpenCode, with one the ledger does not
// record) and break both.
func TestPartialListCoverageSurvivesEveryAgentBackend(t *testing.T) {
	raw, err := os.ReadFile(partialListFixture)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Name   string `json:"name"`
			Result struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("no cases in the partial-list fixture")
	}
	for _, tc := range fixture.Cases {
		var blocks []string
		for _, content := range tc.Result.Content {
			if content.Type != "text" {
				t.Fatalf("%s: unexpected %s content", tc.Name, content.Type)
			}
			blocks = append(blocks, content.Text)
		}
		payload := strings.Join(blocks, "")
		var list struct {
			Items    []json.RawMessage     `json:"items"`
			Coverage *resourceapi.Coverage `json:"coverage"`
		}
		if err := json.Unmarshal([]byte(payload), &list); err != nil || list.Items == nil || list.Coverage == nil || list.Coverage.Status != resourceapi.CoveragePartial || list.Coverage.Read == nil || list.Coverage.Read.Namespaces == nil || len(list.Coverage.Read.Namespaces.Names) == 0 {
			t.Fatalf("%s: the fixture must hold a partial list, got %s", tc.Name, payload)
		}

		for _, backend := range []struct {
			name   string
			parse  func(io.Reader, func(StreamEvent)) Diagnosis
			stream func(blocks []string) string
		}{
			{"claude", parseStream, func(blocks []string) string {
				var parts []map[string]string
				for _, block := range blocks {
					parts = append(parts, map[string]string{"type": "text", "text": block})
				}
				return `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"list","is_error":false,"content":` + jsonText(t, parts) + `}]}}`
			}},
			{"codex", (&codexAgent{bin: "codex"}).parseStream, func(blocks []string) string {
				var parts []map[string]string
				for _, block := range blocks {
					parts = append(parts, map[string]string{"type": "text", "text": block})
				}
				return `{"type":"item.completed","item":{"id":"list","type":"mcp_tool_call","tool":"list_resources","status":"completed","result":{"content":` + jsonText(t, parts) + `}}}`
			}},
			{"cursor", (&cursorAgent{bin: "cursor-agent"}).parseStream, func(blocks []string) string {
				var parts []map[string]map[string]string
				for _, block := range blocks {
					parts = append(parts, map[string]map[string]string{"text": {"text": block}})
				}
				return `{"type":"tool_call","subtype":"completed","tool_call":{"toolCallId":"list","mcpToolCall":{"args":{"toolName":"list_resources"},"result":{"success":{"isError":false,"content":` + jsonText(t, parts) + `}}}}}`
			}},
			{"opencode", (&opencodeAgent{}).parseStream, func(blocks []string) string {
				// OpenCode joins an MCP result's text blocks itself, with a
				// blank line between them.
				return `{"type":"tool_use","part":{"id":"list","tool":"radar_list_resources","state":{"status":"completed","input":{"kind":"services"},"output":` + jsonText(t, strings.Join(blocks, "\n\n")) + `}}}`
			}},
		} {
			t.Run(tc.Name+"/"+backend.name, func(t *testing.T) {
				scope := strings.Repeat("a", 26)
				refs := investigationrefs.NewRegistry()
				lease, err := refs.Begin(scope)
				if err != nil {
					t.Fatal(err)
				}
				defer lease.Close()
				// The investigation transport issues the ref for the producer
				// text and prepends its marker as a block of its own;
				// internal/mcp's TestListResourcesPartialCoverageTransport runs
				// the real middleware over this fixture's result to pin that.
				ref, issued := refs.Issue(scope, payload)
				if !issued {
					t.Fatal("could not issue the evidence ref")
				}
				validator := investigationEvidenceValidator{registry: refs, scope: scope, claimed: map[string]struct{}{}}
				var step *StepInfo
				backend.parse(strings.NewReader(backend.stream(append([]string{investigation.RefMarker(ref)}, blocks...))), func(event StreamEvent) {
					if event.Step != nil && event.Step.Status == "done" {
						step = validator.validate(event).Step
					}
				})
				if step == nil {
					t.Fatal("no completed step")
				}
				if step.Result != payload || step.Truncated {
					t.Fatalf("Findings must receive the producer text unchanged:\n got %q\nwant %q", step.Result, payload)
				}
				if step.IsError == nil || *step.IsError || step.EvidenceRef != ref || !step.RadarEvidence {
					t.Fatalf("the result must stay a confirmed success with Radar provenance: %+v", step)
				}
			})
		}
	}
}

func jsonText(t *testing.T, value any) string {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
