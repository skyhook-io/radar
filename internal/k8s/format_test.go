package k8s

import (
	"testing"
)

func TestTruncate(t *testing.T) {
	tests := []struct {
		s      string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"this is a longer string", 10, "this is..."},
		{"  trimmed  ", 20, "trimmed"},
	}
	for _, tt := range tests {
		got := Truncate(tt.s, tt.maxLen)
		if got != tt.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", tt.s, tt.maxLen, got, tt.want)
		}
	}
}

func TestParseCPUToMillis(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"", 0},
		{"0", 0},
		{"1", 1000},
		{"2", 2000},
		{"250m", 250},
		{"1000m", 1000},
		{"500000000n", 500},
		{"100000000n", 100},
		{"98753u", 99},
		{"1n", 1},
		{"98753000n", 99},
		{"1.5", 1500},
		{"2k", 2000000},
		{"garbage", 0},
	}
	for _, tt := range tests {
		got := ParseCPUToMillis(tt.input)
		if got != tt.want {
			t.Errorf("ParseCPUToMillis(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestParseMemoryToBytes(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"", 0},
		{"0", 0},
		{"1024", 1024},
		{"1024Ki", 1024 * 1024},
		{"256Mi", 256 * 1024 * 1024},
		{"1Gi", 1024 * 1024 * 1024},
		{"2Gi", 2 * 1024 * 1024 * 1024},
		{"1G", 1000 * 1000 * 1000},
		{"1500k", 1500 * 1000},
		{"1.5Gi", 3 * 512 * 1024 * 1024},
		{"1Ti", 1024 * 1024 * 1024 * 1024},
		{"400m", 1},
		{"garbage", 0},
	}
	for _, tt := range tests {
		got := ParseMemoryToBytes(tt.input)
		if got != tt.want {
			t.Errorf("ParseMemoryToBytes(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}
