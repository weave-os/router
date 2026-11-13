package main

import "testing"

func TestParseEnvNonNegativeIntKeepsExplicitZero(t *testing.T) {
	const key = "ROUTER_TEST_NON_NEGATIVE_INT"
	tests := []struct {
		name  string
		raw   string
		unset bool
		want  int
	}{
		{name: "unset falls back", unset: true, want: 3},
		{name: "empty falls back", raw: "", want: 3},
		{name: "explicit zero disables", raw: "0", want: 0},
		{name: "positive value is taken", raw: "5", want: 5},
		{name: "negative falls back", raw: "-1", want: 3},
		{name: "unparseable falls back", raw: "three", want: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.unset {
				t.Setenv(key, tt.raw)
			}
			if got := parseEnvNonNegativeInt(key, 3); got != tt.want {
				t.Fatalf("parseEnvNonNegativeInt(%q) = %d, want %d", tt.raw, got, tt.want)
			}
		})
	}
}

func TestParseEnvIntRejectsZero(t *testing.T) {
	const key = "ROUTER_TEST_POSITIVE_INT"
	t.Setenv(key, "0")
	if got := parseEnvInt(key, 7); got != 7 {
		t.Fatalf("parseEnvInt(\"0\") = %d, want the fallback 7", got)
	}
}
