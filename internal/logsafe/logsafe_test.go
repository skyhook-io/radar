package logsafe

import "testing"

func TestSanitize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"no control chars", "Pod", "Pod"},
		{"empty", "", ""},
		{"newline replaced", "Pod\nlevel=error", "Pod_level=error"},
		{"carriage return replaced", "Pod\rfake=ns", "Pod_fake=ns"},
		{"tab replaced (control char)", "Pod\tx", "Pod_x"},
		{"DEL replaced", "Pod\x7fx", "Pod_x"},
		{"NUL replaced", "Pod\x00x", "Pod_x"},
		{"unicode passes through", "Pôd-ñs", "Pôd-ñs"},
		{"mixed", "a\nb\rc\td", "a_b_c_d"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Sanitize(c.in); got != c.want {
				t.Errorf("Sanitize(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
