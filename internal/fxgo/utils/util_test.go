package utils

import (
	"testing"

	"github.com/google/uuid"
)

func TestCountTrailingZero(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{name: "odd", in: 7, want: 0},
		{name: "one zero", in: 10, want: 1},
		{name: "many zeros", in: 64, want: 6},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CountTrailingZero(tt.in); got != tt.want {
				t.Fatalf("CountTrailingZero(%d) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestGenerateUUID(t *testing.T) {
	first, err := GenerateUUID()
	if err != nil {
		t.Fatalf("GenerateUUID returned error: %v", err)
	}
	second, err := GenerateUUID()
	if err != nil {
		t.Fatalf("GenerateUUID returned error: %v", err)
	}
	if first == second {
		t.Fatalf("GenerateUUID returned duplicate values: %q", first)
	}
	if _, err := uuid.Parse(first); err != nil {
		t.Fatalf("first UUID is invalid: %v", err)
	}
	if _, err := uuid.Parse(second); err != nil {
		t.Fatalf("second UUID is invalid: %v", err)
	}
}
