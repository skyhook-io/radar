package context

import (
	"github.com/skyhook-io/radar/pkg/kagent"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

const kagentGroup = kagent.Group

// kagentURLFields are the URL fields, by kind and path under spec, that
// accept user info or a query, so a token can sit in them.
var kagentURLFields = map[string][][]string{
	kagent.KindModelConfig: {
		{"openAI", "baseUrl"}, {"anthropic", "baseUrl"}, {"mistral", "baseUrl"}, {"gemini", "baseUrl"},
		{"azureOpenAI", "azureEndpoint"}, {"ollama", "host"}, {"sapAICore", "baseUrl"}, {"sapAICore", "authUrl"},
		{"foundry", "endpoint"},
	},
	kagent.KindModelProviderConfig: {{"endpoint"}},
	kagent.KindRemoteMCPServer:     {{"url"}},
	kagent.KindHarness:             {{"substrate", "snapshotPolicy", "location"}},
	kagent.KindSandboxTemplate:     {{"substrate", "snapshotPolicy", "location"}},
}

// redactKagentLiterals removes what in a kagent object can carry a credential
// under any name, so the key-name redaction that follows is not relied on:
//   - literal values: a RemoteMCPServer's spec.headersFrom[].value, a
//     ModelConfig's spec.defaultHeaders values and kagent 1.0.x's
//     spec.azureOpenAI.azureAdToken, and a Harness's or SandboxTemplate's
//     spec.env[].value, which kagent offers no Secret reference for;
//   - URLs: user info, query and fragment of the URL fields above and of
//     skill and plugin artifact sources, and user info (or a credential query)
//     of any URL in other text, condition messages included;
//   - a Harness's workload command and args after a credential-named flag;
//   - a ModelConfig's status.secretHash, an unsalted SHA-256 of its Secrets'
//     bytes (go/core/internal/controller/modelconfig.go:110), which confirms
//     a guessed key.
//
// Names, Secret references and digests stay, so the model still sees what is
// configured and where a credential comes from.
func redactKagentLiterals(obj map[string]any) {
	apiVersion, _ := obj["apiVersion"].(string)
	if resourceid.GroupFromAPIVersion(apiVersion) != kagentGroup {
		return
	}
	if meta, ok := obj["metadata"].(map[string]any); ok {
		if annotations, ok := meta["annotations"].(map[string]any); ok {
			delete(annotations, "kubectl.kubernetes.io/last-applied-configuration")
		}
	}
	kind, _ := obj["kind"].(string)
	spec, _ := obj["spec"].(map[string]any)
	status, _ := obj["status"].(map[string]any)
	// Free text first: the URL fields below then lose their whole query.
	redactURLsInNode(spec)
	redactURLsInNode(status)
	if spec != nil {
		for _, path := range kagentURLFields[kind] {
			stripURLAt(spec, path...)
		}
		switch kind {
		case kagent.KindRemoteMCPServer:
			redactListValues(spec, "headersFrom")
		case kagent.KindModelConfig:
			if headers, ok := spec["defaultHeaders"].(map[string]any); ok {
				for k, v := range headers {
					if s, ok := v.(string); ok && s != "" {
						headers[k] = kagent.Redacted
					}
				}
			}
			if azure, ok := spec["azureOpenAI"].(map[string]any); ok {
				if s, ok := azure["azureAdToken"].(string); ok && s != "" {
					azure["azureAdToken"] = kagent.Redacted
				}
			}
		case kagent.KindHarness:
			redactListValues(spec, "env")
			if workload, ok := spec["workload"].(map[string]any); ok {
				for _, field := range []string{"command", "args"} {
					if list, ok := workload[field].([]any); ok {
						workload[field] = redactArgList(list)
					}
				}
			}
		case kagent.KindSandboxTemplate:
			redactListValues(spec, "env")
		case kagent.KindAgentTemplate:
			stripArtifactURLs(spec)
		case kagent.KindAgent:
			if template, ok := spec["template"].(map[string]any); ok {
				if inline, ok := template["inline"].(map[string]any); ok {
					stripArtifactURLs(inline)
				}
			}
		}
	}
	if kind == kagent.KindModelConfig && status != nil {
		delete(status, "secretHash")
	}
}

// redactListValues blanks the literal value of each {name, value} entry.
func redactListValues(spec map[string]any, field string) {
	entries, _ := spec[field].([]any)
	for _, e := range entries {
		if entry, ok := e.(map[string]any); ok {
			if s, ok := entry["value"].(string); ok && s != "" {
				entry["value"] = kagent.Redacted
			}
		}
	}
}

func stripURLAt(node map[string]any, path ...string) {
	for _, key := range path[:len(path)-1] {
		next, ok := node[key].(map[string]any)
		if !ok {
			return
		}
		node = next
	}
	last := path[len(path)-1]
	if s, ok := node[last].(string); ok && s != "" {
		node[last] = kagent.StripURLCredentials(s)
	}
}

// stripArtifactURLs strips an AgentTemplate spec's skill and plugin sources:
// a Git URL or an S3 endpoint can carry a token or a presigned signature.
func stripArtifactURLs(templateSpec map[string]any) {
	for _, field := range []string{"skills", "plugins"} {
		items, _ := templateSpec[field].([]any)
		for _, item := range items {
			entry, ok := item.(map[string]any)
			if !ok {
				continue
			}
			source, ok := entry["source"].(map[string]any)
			if !ok {
				continue
			}
			stripURLAt(source, "git", "url")
			stripURLAt(source, "bucket", "s3", "endpoint")
		}
	}
}

func redactArgList(list []any) []any {
	args := make([]string, 0, len(list))
	for _, a := range list {
		s, ok := a.(string)
		if !ok {
			return list
		}
		args = append(args, s)
	}
	out := make([]any, len(args))
	for i, a := range kagent.RedactArgs(args) {
		out[i] = a
	}
	return out
}

func redactURLsInNode(node any) any {
	switch v := node.(type) {
	case map[string]any:
		for k, val := range v {
			v[k] = redactURLsInNode(val)
		}
		return v
	case []any:
		for i, item := range v {
			v[i] = redactURLsInNode(item)
		}
		return v
	case string:
		return kagent.RedactURLsInText(v)
	}
	return node
}
