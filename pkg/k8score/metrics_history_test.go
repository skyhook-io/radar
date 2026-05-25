package k8score

import "testing"

func TestParseCPU(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"", 0},
		{"137492n", 137492},
		{"188u", 188000},
		{"250m", 250000000},
		{"1", 1000000000},
	}

	for _, tt := range tests {
		if got := parseCPU(tt.input); got != tt.want {
			t.Errorf("parseCPU(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}
