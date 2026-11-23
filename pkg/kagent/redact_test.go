package kagent

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

// testdata/redaction.json is shared with k8s-ui's resource-utils-kagent.test.ts.
func TestRedactionVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/redaction.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Fn   string          `json:"fn"`
		Path string          `json:"path"`
		In   json.RawMessage `json:"in"`
		Want json.RawMessage `json:"want"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		switch c.Fn {
		case "url", "text", "cell":
			var in, want string
			_ = json.Unmarshal(c.In, &in)
			_ = json.Unmarshal(c.Want, &want)
			got := StripURLCredentials(in)
			switch c.Fn {
			case "text":
				got = RedactURLsInText(in)
			case "cell":
				got = RedactPrinterCell(c.Path, in)
			}
			if got != want {
				t.Errorf("%s(%q) = %q, want %q", c.Fn, in, got, want)
			}
		case "args":
			var in, want []string
			_ = json.Unmarshal(c.In, &in)
			_ = json.Unmarshal(c.Want, &want)
			if got := RedactArgs(in); !slices.Equal(got, want) {
				t.Errorf("args(%q) = %q, want %q", in, got, want)
			}
		default:
			t.Fatalf("unknown fn %q", c.Fn)
		}
	}
}
