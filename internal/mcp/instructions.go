package mcp

import "github.com/skyhook-io/radar/pkg/investigation"

// serverInstructions is the text a server registering the given tool set sends
// at initialize: Radar's general guidance (pkg/investigation/guidance.go), with
// the write-tool routing only where the write tools are registered.
func serverInstructions(includeWrites bool) string {
	return investigation.GeneralGuidance(includeWrites)
}
