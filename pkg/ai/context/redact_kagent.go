package context

import "github.com/skyhook-io/radar/pkg/resourceid"

const kagentGroup = "api.kagent.dev"

// redactKagentLiterals blanks the kagent fields that hold literal values a
// credential can be put in under any name, so pattern redaction is not
// enough: a RemoteMCPServer's spec.headersFrom[].value, a ModelConfig's
// spec.defaultHeaders values and kagent 1.0.x's
// spec.azureOpenAI.azureAdToken, and the runtime environment of a Harness or
// SandboxTemplate (spec.env[].value), which kagent offers no Secret reference
// for. Names and Secret references stay, so the model still sees what is
// configured and where a credential comes from.
func redactKagentLiterals(obj map[string]any) {
	apiVersion, _ := obj["apiVersion"].(string)
	if resourceid.GroupFromAPIVersion(apiVersion) != kagentGroup {
		return
	}
	spec, _ := obj["spec"].(map[string]any)
	if spec == nil {
		return
	}
	switch kind, _ := obj["kind"].(string); kind {
	case "RemoteMCPServer":
		redactListValues(spec, "headersFrom")
	case "ModelConfig":
		if headers, ok := spec["defaultHeaders"].(map[string]any); ok {
			for k, v := range headers {
				if s, ok := v.(string); ok && s != "" {
					headers[k] = "[REDACTED]"
				}
			}
		}
		if azure, ok := spec["azureOpenAI"].(map[string]any); ok {
			if s, ok := azure["azureAdToken"].(string); ok && s != "" {
				azure["azureAdToken"] = "[REDACTED]"
			}
		}
	case "Harness", "SandboxTemplate":
		redactListValues(spec, "env")
	}
}

// redactListValues blanks the literal value of each {name, value} entry.
func redactListValues(spec map[string]any, field string) {
	entries, _ := spec[field].([]any)
	for _, e := range entries {
		if entry, ok := e.(map[string]any); ok {
			if s, ok := entry["value"].(string); ok && s != "" {
				entry["value"] = "[REDACTED]"
			}
		}
	}
}
