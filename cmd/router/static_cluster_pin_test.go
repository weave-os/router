package main

import (
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseStaticClusterPinsSkipsUnknownModels(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	got := parseStaticClusterPins("0:claude-haiku-4-5,1:not-in-catalog", logger)

	require.Equal(t, map[int]string{0: "claude-haiku-4-5"}, got)
}

func TestParseStaticClusterPinsPanicsOnMalformedEntry(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	assert.Panics(t, func() {
		parseStaticClusterPins("not-a-pair", logger)
	})
}
