package router_test

import (
	"testing"

	"workweave/router/internal/router"

	"github.com/stretchr/testify/assert"
)

func TestCanonicalizeEffort(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "low", want: "low"},
		{input: "LOW", want: "low"},
		{input: "fast", want: "low"},
		{input: "minimal", want: "low"},
		{input: "min", want: "low"},
		{input: "medium", want: "medium"},
		{input: "med", want: "medium"},
		{input: "high", want: "high"},
		{input: "max", want: "max"},
		{input: "xhigh", want: "xhigh"},
		{input: "ultra", want: "xhigh"},
		{input: "ULTRA", want: "xhigh"},
		{input: "garbage", want: "garbage"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, router.CanonicalizeEffort(tt.input))
		})
	}
}

func TestIsValidEffort(t *testing.T) {
	for _, level := range []string{"low", "medium", "high", "max", "xhigh", "fast", "minimal", "ultra", "min", "med"} {
		t.Run(level, func(t *testing.T) {
			assert.True(t, router.IsValidEffort(level))
		})
	}
	for _, level := range []string{"garbage", ""} {
		t.Run(level+"_invalid", func(t *testing.T) {
			assert.False(t, router.IsValidEffort(level))
		})
	}
}
