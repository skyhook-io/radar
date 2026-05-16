package logsafe

import "testing"

func TestEstimateTokens(t *testing.T) {
	// English-JSON heuristic: 4 chars/token. Cheap and deterministic.
	cases := []struct {
		bytes int
		want  int
	}{
		{0, 0},
		{1, 0},
		{4, 1},
		{2156, 539},
	}
	for _, c := range cases {
		if got := EstimateTokens(c.bytes); got != c.want {
			t.Errorf("EstimateTokens(%d) = %d, want %d", c.bytes, got, c.want)
		}
	}
}

func TestSanitize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// Legitimate values pass through unchanged.
		{"plain ASCII passes", "Pod", "Pod"},
		{"empty", "", ""},
		{"unicode passes through", "Pôd-ñs", "Pôd-ñs"},
		{"dash and digits pass through", "my-svc-7", "my-svc-7"},

		// Multi-line injection (newlines / CR / control chars / DEL).
		{"newline replaced", "Pod\nlevel=error", "Pod_level_error"},
		{"carriage return replaced", "Pod\rfake=ns", "Pod_fake_ns"},
		{"tab replaced (control char)", "Pod\tx", "Pod_x"},
		{"DEL replaced", "Pod\x7fx", "Pod_x"},
		{"NUL replaced", "Pod\x00x", "Pod_x"},

		// Same-line logfmt field injection — space and '=' would otherwise
		// introduce new key=value tokens into the log line.
		{"space replaced", "Pod level=error", "Pod_level_error"},
		{"equals replaced", "kind=Pod", "kind_Pod"},
		{"full logfmt injection neutralized", "Pod level=error fake=injected", "Pod_level_error_fake_injected"},

		// Mixed.
		{"newline + space + equals all replaced", "a\nb c=d", "a_b_c_d"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Sanitize(c.in); got != c.want {
				t.Errorf("Sanitize(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
