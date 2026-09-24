package poll

import (
	"reflect"
	"strings"
	"testing"
)

func choices(ids ...string) Answer { return Answer{Choices: ids} }

func TestNormalizeRejectsWhatTheUICantProduce(t *testing.T) {
	cases := map[string]map[string]Answer{
		"unknown question":           {"q42": choices("x")},
		"unknown option":             {"q2": choices("nine")},
		"two answers to single":      {"q2": choices("one", "two_three")},
		"none combined with others":  {"q4": choices("none", "screenshots")},
		"choices on a text question": {"q7": choices("x")},
		"text too long":              {"q7": {Text: strings.Repeat("a", MaxTextRunes+1)}},
	}
	for name, answers := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Current.Normalize(answers, "local"); err == nil {
				t.Fatal("Normalize succeeded, want error")
			}
		})
	}
}

func TestNormalizeDropsStrandedAnswers(t *testing.T) {
	got, err := Current.Normalize(map[string]Answer{
		"q5":         choices("no_ai"),
		"q5_agent":   choices("codex"),
		"q5_blocker": choices("no_api"),
		"q3c_people": choices("two_five"),
		"q3":         choices("just_me"),
	}, "local")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Answer{"q5": choices("no_ai"), "q3": choices("just_me")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestNormalizeInClusterSwapsQ3(t *testing.T) {
	got, err := Current.Normalize(map[string]Answer{
		"q3":         choices("just_me"),
		"q3c_people": choices("two_five"),
		"q3c_front":  choices("oidc"),
	}, "in-cluster")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["q3"]; ok {
		t.Fatal("q3 kept in-cluster")
	}
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
}

func TestNormalizeHostedAgentRecordsCloudUse(t *testing.T) {
	got, err := Current.Normalize(map[string]Answer{
		"q5":       choices("regularly"),
		"q5_agent": choices("claude_code", "radar_cloud"),
		"q9":       choices("price"),
	}, "local")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got["q9"], choices("already")) {
		t.Fatalf("q9 = %+v, want recorded as already", got["q9"])
	}
}

func TestNormalizeText(t *testing.T) {
	got, err := Current.Normalize(map[string]Answer{
		"q1": {Choices: []string{"debug", "debug"}, Text: "ignored without other"},
		"q6": {Choices: []string{"very"}, Text: "  the topology  "},
		"q9": {Choices: []string{"missing"}, Text: "SAML groups"},
		"q7": {Text: "   "},
	}, "local")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Answer{
		"q1": choices("debug"),
		"q6": {Choices: []string{"very"}, Text: "the topology"},
		"q9": {Choices: []string{"missing"}, Text: "SAML groups"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestNormalizeIgnoresOtherBlocks(t *testing.T) {
	r := Current
	r.Block = "B"
	got, err := r.Normalize(map[string]Answer{"a_role": choices("developer")}, "local")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("block A answer kept in a block B round: %+v", got)
	}
}
