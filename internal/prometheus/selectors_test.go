package prometheus

import (
	"reflect"
	"testing"
)

func bare(metric string) Selector { return Selector{Metric: metric, Matchers: []Matcher{}} }

func withMatchers(metric string, ms ...Matcher) Selector {
	return Selector{Metric: metric, Matchers: ms}
}

func TestExtractSelectors(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  []Selector
	}{
		{"bare metric", "up", []Selector{bare("up")}},
		{"recording rule name", `job:http_requests:rate5m{job="api"}`,
			[]Selector{withMatchers("job:http_requests:rate5m", Matcher{"job", "=", "api"})}},
		{"matchers with all operators",
			`http_requests_total{namespace="payments", pod=~"api-.*", container!="", job!~"x|y"}`,
			[]Selector{withMatchers("http_requests_total",
				Matcher{"namespace", "=", "payments"}, Matcher{"pod", "=~", "api-.*"},
				Matcher{"container", "!=", ""}, Matcher{"job", "!~", "x|y"})}},
		{"trailing comma and empty braces", `a{x="1",} + b{}`,
			[]Selector{withMatchers("a", Matcher{"x", "=", "1"}), bare("b")}},
		{"bare matcher block", `{__name__=~"node_cpu.*", mode="idle"}`,
			[]Selector{withMatchers("", Matcher{"__name__", "=~", "node_cpu.*"}, Matcher{"mode", "=", "idle"})}},
		{"nested functions", "scalar(sum(machine_memory_bytes))", []Selector{bare("machine_memory_bytes")}},
		{"rate with range", "rate(container_cpu_usage_seconds_total[5m])",
			[]Selector{bare("container_cpu_usage_seconds_total")}},
		{"string literals containing braces and parens",
			`label_replace(up, "dst{", "{$1}", "src", "(.*){")`, []Selector{bare("up")}},
		{"string literal with escaped quote and brace", `label_join(up, "x", "\"{", "a")`, []Selector{bare("up")}},
		{"count_values string first arg", `count_values("version", build_info)`, []Selector{bare("build_info")}},
		{"aggregation by clause", "sum by (pod, namespace) (rate(x[5m]))", []Selector{bare("x")}},
		{"aggregation without clause no space", "sum without(pod)(x)", []Selector{bare("x")}},
		{"trailing by clause", "sum(rate(x[1m])) by (le)", []Selector{bare("x")}},
		{"keywords are case-insensitive", "SUM BY (pod) (foo)", []Selector{bare("foo")}},
		{"vector matching with group_left labels",
			"a / on(pod) group_left(node) kube_pod_info", []Selector{bare("a"), bare("kube_pod_info")}},
		{"group_left without labels", "a * on(pod) group_left b", []Selector{bare("a"), bare("b")}},
		{"ignoring clause", "a - ignoring(instance) b", []Selector{bare("a"), bare("b")}},
		{"bool modifier", "a > bool b", []Selector{bare("a"), bare("b")}},
		{"set operators", "a and b or c unless d", []Selector{bare("a"), bare("b"), bare("c"), bare("d")}},
		{"atan2 operator", "a atan2 b", []Selector{bare("a"), bare("b")}},
		{"set-operator words as metric names", "or / and + unless", []Selector{bare("or"), bare("and"), bare("unless")}},
		{"set operator after paren and range", "(a) or b[5m] and c", []Selector{bare("a"), bare("b"), bare("c")}},
		{"set operator after number and string", `1 or "x" unless d`, []Selector{bare("d")}},
		{"keyword metric hidden behind a scoped selector",
			`up{namespace="payments",pod="api-1"} or (by)`,
			[]Selector{withMatchers("up", Matcher{"namespace", "=", "payments"}, Matcher{"pod", "=", "api-1"}), bare("by")}},
		{"clause words as metric names", "(without) + offset", []Selector{bare("without"), bare("offset")}},
		{"aggregator words as metric names", "(sum) / count + topk", []Selector{bare("sum"), bare("count"), bare("topk")}},
		{"adjacent operands over-report rather than hide", "sum foo", []Selector{bare("sum"), bare("foo")}},
		{"aggregator clause after aggregation", "count(foo) by (a) / sum without (b) (bar)", []Selector{bare("foo"), bare("bar")}},
		{"range forms", "a[300] + b[1h30m:30s] + c[1h:] + d[1.5]", []Selector{bare("a"), bare("b"), bare("c"), bare("d")}},
		{"exponent numbers", "x > 1e-3 and x < 2E3", []Selector{bare("x"), bare("x")}},
		{"underscore separators", `up{namespace="p",pod="a"} > 1_000 or up{namespace="p",pod="a"} < 0x_FF`,
			[]Selector{withMatchers("up", Matcher{"namespace", "=", "p"}, Matcher{"pod", "=", "a"}),
				withMatchers("up", Matcher{"namespace", "=", "p"}, Matcher{"pod", "=", "a"})}},
		{"comment between metric and matchers", "up # c\n{namespace=\"p\"} / sum # c\n by (pod) # c\n (foo) + sum # c\n(bar)",
			[]Selector{withMatchers("up", Matcher{"namespace", "=", "p"}), bare("foo"), bare("bar")}},
		{"comment between clause keyword and label list", "a * on # c\n(pod) group_left # c\n (node) b",
			[]Selector{bare("a"), bare("b")}},
		{"Inf and NaN complete an operand", `x > Inf or y{namespace="p"} == NaN and z`,
			[]Selector{bare("x"), withMatchers("y", Matcher{"namespace", "=", "p"}), bare("z")}},
		{"comment ended by carriage return", "up{namespace=\"p\"} # comment\ror node_cpu_seconds_total",
			[]Selector{withMatchers("up", Matcher{"namespace", "=", "p"}), bare("node_cpu_seconds_total")}},
		{"set operator after postfix by clause",
			`sum(up{namespace="p",pod="a"}) by (pod) or up{namespace="p",pod="b"}`,
			[]Selector{withMatchers("up", Matcher{"namespace", "=", "p"}, Matcher{"pod", "=", "a"}),
				withMatchers("up", Matcher{"namespace", "=", "p"}, Matcher{"pod", "=", "b"})}},
		{"keyword metric after postfix by clause", "sum(a) without (pod) or by", []Selector{bare("a"), bare("by")}},
		{"clause after vector-matching labels starts an operand", "a * on(pod) or", []Selector{bare("a"), bare("or")}},
		{"offset", "foo offset 5m", []Selector{bare("foo")}},
		{"negative offset after matchers", `foo{a="b"} offset -1h`, []Selector{withMatchers("foo", Matcher{"a", "=", "b"})}},
		{"at modifier timestamp", "foo @ 1609746000", []Selector{bare("foo")}},
		{"at modifier start()", "foo @ start()", []Selector{bare("foo")}},
		{"at modifier end() after range", "rate(foo[5m] @ end())", []Selector{bare("foo")}},
		{"subquery", "max_over_time(rate(foo[5m])[1h:1m])", []Selector{bare("foo")}},
		{"subquery default step", "(sum(rate(x[1m])))[30m:]", []Selector{bare("x")}},
		{"histogram quantile", "histogram_quantile(0.9, sum by (le) (rate(h_bucket[5m])))", []Selector{bare("h_bucket")}},
		{"numbers and Inf/NaN", "x * 1e3 + 0x1F - Inf + NaN", []Selector{bare("x")}},
		{"comparison with float", "rate(foo[5m]) > .5", []Selector{bare("foo")}},
		{"no selectors", "vector(1) + time()", []Selector{}},
		{"scalar literal only", "42", []Selector{}},
		{"comment", "up # {not a selector}", []Selector{bare("up")}},
		{"regex with escaped double quote", `foo{name=~"a\"b.*"}`,
			[]Selector{withMatchers("foo", Matcher{"name", "=~", `a"b.*`})}},
		{"regex with escaped backslash", `foo{name=~"a\\.b"}`,
			[]Selector{withMatchers("foo", Matcher{"name", "=~", `a\.b`})}},
		{"single-quoted with escaped quote and inner double quote", `foo{name='it\'s "x"'}`,
			[]Selector{withMatchers("foo", Matcher{"name", "=", `it's "x"`})}},
		{"backtick raw string", "foo{re=`a\\.b\"c`}",
			[]Selector{withMatchers("foo", Matcher{"re", "=", `a\.b"c`})}},
		{"unicode in value", `foo{msg="héllo"}`, []Selector{withMatchers("foo", Matcher{"msg", "=", "héllo"})}},
		{"topk", `topk(5, foo{a="b"})`, []Selector{withMatchers("foo", Matcher{"a", "=", "b"})}},
		{"absent", `absent(foo{job="x"})`, []Selector{withMatchers("foo", Matcher{"job", "=", "x"})}},
		{"unary minus", "-foo", []Selector{bare("foo")}},
		{"keyword-named metric with matchers", `on{namespace="x"}`, []Selector{withMatchers("on", Matcher{"namespace", "=", "x"})}},
		{"multiline", "sum(\n  rate(a[5m])\n) /\nsum(rate(b[5m]))", []Selector{bare("a"), bare("b")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, unknown := ExtractSelectors(tc.query)
			if unknown {
				t.Fatalf("ExtractSelectors(%q) unknown=true, want selectors %v", tc.query, tc.want)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ExtractSelectors(%q)\n got %#v\nwant %#v", tc.query, got, tc.want)
			}
		})
	}
}

func TestExtractSelectorsUnknown(t *testing.T) {
	cases := []struct {
		name  string
		query string
	}{
		{"unclosed brace", `x{a="b"`},
		{"unterminated string", `x{a="b}`},
		{"unterminated single quote", `x{a='b}`},
		{"unterminated backtick", "x{a=`b}"},
		{"bare matcher value", `x{a=b}`},
		{"missing operator", `x{a "b"}`},
		{"quoted metric name in braces", `{"http.requests", a="b"}`},
		{"templating variable in range", "rate(foo[$__interval])"},
		{"empty range", "foo[]"},
		{"unclosed range", "foo[5m"},
		{"unbalanced open paren", "(foo"},
		{"unbalanced close paren", "foo)"},
		{"dotted name", "foo.bar"},
		{"stray character", "foo ; bar"},
		{"unclosed label list", "sum by (pod (foo)"},
		{"bad label in list", `sum by (pod=1) (foo)`},
		{"bad escape in value", `foo{a="\q"}`},
		{"newline inside string", "foo{a=\"b\nc\"}"},
		{"unclosed string argument", `label_replace(up, "dst`},
		{"comment inside matchers", "foo{a=\"b\" # c\n}"},
		{"comment inside label list", "sum by (a # c\n) (foo)"},
		{"identifier in range", `rate(up{namespace="payments",pod="api-1"}[other_metric])`},
		{"duration expression in range", "foo[2*5m]"},
		{"nested subquery colons", "foo[1h:1m:1s]"},
		{"range with leading colon", "foo[:1m]"},
		{"number running into identifier", "123garbage"},
		{"duration running into identifier", "foo offset 5mfoo"},
		{"bad duration unit", "foo[5x]"},
		{"dangling exponent", "foo * 1e"},
		{"bare hex prefix", "foo * 0x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, unknown := ExtractSelectors(tc.query)
			if !unknown {
				t.Fatalf("ExtractSelectors(%q) unknown=false, got %#v", tc.query, got)
			}
			if got == nil || len(got) != 0 {
				t.Fatalf("ExtractSelectors(%q) must return an empty, non-nil list when unknown, got %#v", tc.query, got)
			}
		})
	}
}
