package mcp

import (
	"fmt"
	"reflect"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/jsonpath"
)

type waitJSONPath struct {
	path   string
	value  string
	exists bool
	parser *jsonpath.JSONPath
}

func parseWaitJSONPath(input string) (*waitJSONPath, error) {
	parts := splitJSONPathInput(input)
	if len(parts) > 2 {
		return nil, fmt.Errorf("jsonpath must be jsonpath={.status.field}=value or jsonpath={.status.field}")
	}
	expression := strings.Trim(parts[0], `'"`)
	path := strings.TrimSuffix(strings.TrimPrefix(expression, "{"), "}")
	if path != ".status" && !strings.HasPrefix(path, ".status.") && !strings.HasPrefix(path, ".status[") {
		return nil, fmt.Errorf("jsonpath must start with .status; wait_for reads only status")
	}
	if strings.ContainsAny(path, "{}") {
		return nil, fmt.Errorf("jsonpath could not be evaluated against .status: expected one path expression")
	}
	p := jsonpath.New("wait_for").AllowMissingKeys(true)
	if err := p.Parse("{" + path + "}"); err != nil {
		return nil, fmt.Errorf("jsonpath could not be evaluated against .status: invalid path syntax")
	}
	j := &waitJSONPath{path: path, exists: len(parts) == 1, parser: p}
	if len(parts) == 2 {
		j.value = strings.Trim(parts[1], `'"`)
		if j.value == "" {
			return nil, fmt.Errorf("jsonpath wait has to have a value after equal sign")
		}
	}
	return j, nil
}

func (j *waitJSONPath) evaluate(obj *unstructured.Unstructured) (met bool, observed string, missing bool, err error) {
	status, _, _ := unstructured.NestedFieldNoCopy(obj.Object, "status")
	results, e := j.parser.FindResults(map[string]any{"status": status})
	if e != nil {
		return false, "", false, fmt.Errorf("jsonpath could not be evaluated against .status: invalid operation at path expression")
	}
	var values []reflect.Value
	for _, r := range results {
		values = append(values, r...)
	}
	if len(values) == 0 {
		return false, j.path + " not present", true, nil
	}
	if len(values) != 1 {
		return false, "", false, fmt.Errorf("jsonpath could not be evaluated against .status: expected exactly one scalar result")
	}
	v := values[0]
	for v.IsValid() && (v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer) {
		v = v.Elem()
	}
	if !v.IsValid() {
		return false, j.path + " not present", true, nil
	}
	switch v.Kind() {
	case reflect.Map, reflect.Slice, reflect.Array, reflect.Struct:
		return false, "", false, fmt.Errorf("jsonpath could not be evaluated against .status: expected exactly one scalar result")
	}
	value := fmt.Sprint(v.Interface())
	return j.exists || value == j.value, truncateCrashLogLine(j.path+"="+value, 120), false, nil
}

// Ported from kubectl v0.37.1 pkg/cmd/wait/wait.go (Apache-2.0).
// Single '=' separates the expression and value; '==' inside filters stays intact.
func splitJSONPathInput(input string) []string {
	var output []string
	var element strings.Builder
	for i := 0; i < len(input); i++ {
		if input[i] == '=' {
			if i < len(input)-1 && input[i+1] == '=' {
				element.WriteString("==")
				i++
				continue
			}
			output = append(output, element.String())
			element.Reset()
			continue
		}
		element.WriteByte(input[i])
	}
	return append(output, element.String())
}
