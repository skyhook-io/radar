package k8s

import (
	"testing"
	"time"
)

func TestFormatAge(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{5 * time.Minute, "5m"},
		{3 * time.Hour, "3h"},
		{48 * time.Hour, "2d"},
	}
	for _, tt := range tests {
		got := FormatAge(tt.d)
		if got != tt.want {
			t.Errorf("FormatAge(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

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
	}
	for _, tt := range tests {
		got := ParseMemoryToBytes(tt.input)
		if got != tt.want {
			t.Errorf("ParseMemoryToBytes(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}
