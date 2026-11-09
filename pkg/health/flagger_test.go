package health

import (
	"encoding/json"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"os"
	"testing"
)

func TestFlaggerContract(t *testing.T) {
	raw, err := os.ReadFile("testdata/flagger.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name   string
		Object unstructured.Unstructured
		Level  Level
		Text   string
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			v := FlaggerCanary(&tc.Object)
			if v.Level != tc.Level || v.Reason != tc.Text {
				t.Fatalf("got %+v, want %s/%s", v, tc.Level, tc.Text)
			}
		})
	}
}
