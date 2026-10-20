package mcp

import (
	"strings"

	"github.com/skyhook-io/radar/pkg/investigation"
)

// serverInstructions is the text a server registering the given tool set sends
// when a client connects, in the initialize result or, on protocol 2026-07-28,
// the server/discover result: Radar's general guidance
// (pkg/investigation/guidance.go), with the write-tool routing only where the
// write tools are registered.
func serverInstructions(includeWrites bool) string {
	text := investigation.GeneralGuidance(includeWrites)
	if includeWrites && execToolsEnabled.Load() {
		text = strings.Replace(text, "manage_cronjob, manage_node.", "manage_cronjob, manage_node; exec_pod for in-container checks.", 1)
		text = strings.Replace(text, "ideally by restoring what changed and broke", "ideally restoring the breaking change", 1)
	}
	return text
}
