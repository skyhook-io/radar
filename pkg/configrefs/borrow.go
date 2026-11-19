package configrefs

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Reference extraction never mutates nested values; borrowing avoids copying
// whole route trees on every informer update.
func borrowedSlice(obj map[string]any, fields ...string) ([]any, bool, error) {
	raw, found, err := unstructured.NestedFieldNoCopy(obj, fields...)
	if err != nil || !found {
		return nil, found, err
	}
	value, ok := raw.([]any)
	if !ok {
		return nil, false, fmt.Errorf("%v is not a slice", fields)
	}
	return value, true, nil
}

func borrowedMap(obj map[string]any, fields ...string) (map[string]any, bool, error) {
	raw, found, err := unstructured.NestedFieldNoCopy(obj, fields...)
	if err != nil || !found {
		return nil, found, err
	}
	value, ok := raw.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("%v is not a map", fields)
	}
	return value, true, nil
}
