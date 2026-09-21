package mcp

import "github.com/google/jsonschema-go/jsonschema"

func costInputSchema() *jsonschema.Schema {
	schema, err := jsonschema.For[getCostInput](nil)
	if err != nil {
		panic(err)
	}
	schema.Properties["view"].Enum = []any{"summary", "workloads", "nodes", "trend"}
	schema.Properties["view"].Default = []byte(`"summary"`)
	schema.Properties["range"].Enum = []any{"6h", "24h", "7d"}
	schema.Properties["include_points"].Type = "boolean"
	schema.Properties["include_points"].Types = nil
	minLimit, maxLimit := float64(1), float64(costMaxLimit)
	schema.Properties["limit"].Minimum = &minLimit
	schema.Properties["limit"].Maximum = &maxLimit
	// The SDK inserts schema defaults before calling the handler. Defaults on
	// view-specific fields would make an omitted field look explicitly supplied.
	return schema
}

func rightsizingInputSchema() *jsonschema.Schema {
	schema, err := jsonschema.For[getRightsizingInput](nil)
	if err != nil {
		panic(err)
	}
	schema.Properties["scope"].Enum = []any{"workload", "namespace", "cluster"}
	schema.Properties["kind"].Enum = []any{"Deployment", "StatefulSet", "DaemonSet"}
	schema.Properties["classification"].Enum = []any{"reduction", "increase", "review", "need_data", "in_range"}
	schema.Properties["include_all"].Default = []byte(`false`)
	names := schema.Properties["namespaces"]
	names.Type, names.Types = "array", nil
	minItems, minLength := 1, 1
	names.MinItems = &minItems
	names.Items.MinLength = &minLength
	minLimit, maxLimit := float64(1), float64(rightsizingMaxLimit)
	schema.Properties["limit"].Minimum = &minLimit
	schema.Properties["limit"].Maximum = &maxLimit
	return schema
}
